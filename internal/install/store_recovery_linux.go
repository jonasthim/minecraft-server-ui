//go:build linux

package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// RecoverPending promotes one complete, authenticated transaction journal
// publication left by a process that stopped before its atomic rename. Load
// remains read-only and continues to refuse a pending slot.
func (s *journalStore) RecoverPending(ctx context.Context) (retErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(ctx); err != nil {
		return err
	}
	pendingInfo, pendingRaw, err := s.readPending(ctx)
	if errors.Is(err, os.ErrNotExist) {
		return s.verifyRoot(ctx)
	}
	if err != nil {
		return err
	}
	next, err := decodeJournal(pendingRaw)
	if err != nil {
		return fmt.Errorf("decode pending installer transaction: %w", err)
	}
	if err := validateTransaction(&next, next.Mode, next.Phase); err != nil {
		return fmt.Errorf("validate pending installer transaction: %w", err)
	}
	currentRaw, currentInfo, currentPresent, err := s.readJournal(ctx)
	if err != nil {
		return err
	}
	if currentPresent {
		current, decodeErr := decodeJournal(currentRaw)
		if decodeErr != nil || validateTransaction(&current, current.Mode, current.Phase) != nil ||
			!sameImmutableTransaction(current, next) || !validPendingTransition(current.Phase, next.Phase) {
			return errors.New("pending installer transaction does not follow the current journal")
		}
	} else if next.Phase != phasePrepared || next.FailedStage != "" {
		return errors.New("pending installer transaction has no valid initial journal preimage")
	}
	if _, err := s.readIntentLocked(ctx, next); err != nil {
		return fmt.Errorf("verify pending installer transaction intent: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.verifyRoot(ctx); err != nil {
		return err
	}
	if s.config.beforePublish != nil {
		if err := s.config.beforePublish(); err != nil {
			return fmt.Errorf("installer pending recovery prepublication fence: %w", err)
		}
	}
	// Re-read every publication operand immediately before the atomic CAS.
	latestPendingInfo, latestPendingRaw, err := s.readPending(ctx)
	if err != nil || !os.SameFile(latestPendingInfo, pendingInfo) || !bytes.Equal(latestPendingRaw, pendingRaw) {
		return errors.Join(errors.New("installer journal pending changed during recovery"), err)
	}
	if _, err := s.readIntentLocked(ctx, next); err != nil {
		return fmt.Errorf("recheck pending installer transaction intent: %w", err)
	}
	latestRaw, latestInfo, latestPresent, err := s.readJournal(ctx)
	if err != nil {
		return err
	}
	if latestPresent != currentPresent || latestPresent && (!bytes.Equal(latestRaw, currentRaw) || !os.SameFile(latestInfo, currentInfo)) {
		return errors.New("installer journal changed during pending recovery")
	}
	if err := s.verifyRoot(ctx); err != nil {
		return err
	}
	latestPendingInfo, latestPendingRaw, err = s.readPending(ctx)
	if err != nil || !os.SameFile(latestPendingInfo, pendingInfo) || !bytes.Equal(latestPendingRaw, pendingRaw) {
		return errors.Join(errors.New("installer journal pending changed before publication"), err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.uncertain = true
	if err := s.root.Rename(pendingName, journalName); err != nil {
		s.uncertain = false
		return fmt.Errorf("promote recovered installer transaction: %w", err)
	}
	if err := syncDirectory(s.root); err != nil {
		return fmt.Errorf("sync recovered installer transaction; publication is uncertain: %w", err)
	}
	if err := s.verifyRoot(ctx); err != nil {
		return fmt.Errorf("recheck installer state root after pending recovery: %w", err)
	}
	publishedRaw, publishedInfo, present, err := s.readJournal(ctx)
	if err != nil || !present || !bytes.Equal(publishedRaw, pendingRaw) || !os.SameFile(publishedInfo, pendingInfo) {
		return errors.Join(errors.New("recovered installer transaction publication is uncertain"), err)
	}
	if _, err := s.root.Lstat(pendingName); !errors.Is(err, os.ErrNotExist) {
		return errors.Join(errors.New("pending installer slot remains after publication"), err)
	}
	s.snapshot = &journalSnapshot{present: true, raw: bytes.Clone(pendingRaw), info: publishedInfo, tx: *cloneTransaction(&next)}
	s.uncertain = false
	return nil
}

func validPendingTransition(from, to phase) bool {
	switch from {
	case phasePrepared:
		return to == phaseQuiescing || to == phaseRollingBack
	case phaseQuiescing:
		return to == phaseStopped || to == phaseRollingBack
	case phaseStopped:
		return to == phaseReplacing || to == phaseRollingBack
	case phaseReplacing:
		return to == phasePublished || to == phaseRollingBack
	case phasePublished:
		return to == phaseVerifying || to == phaseRollingBack
	case phaseVerifying:
		return to == phaseCommitted || to == phaseRollingBack
	case phaseRollingBack:
		return to == phaseRolledBack || to == phaseManualRepair
	case phaseManualRepair:
		return to == phaseRollingBack
	default:
		return false
	}
}

func (s *journalStore) readPending(ctx context.Context) (os.FileInfo, []byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	info, err := s.root.Lstat(pendingName)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect installer journal pending slot: %w", err)
	}
	if err := checkProtectedFile(info, s.expectedUID()); err != nil || info.Mode().Perm() != 0600 || info.Size() <= 0 || info.Size() > maxJournalBytes {
		return nil, nil, errors.Join(errors.New("installer journal pending file is unsafe"), err)
	}
	file, err := s.root.Open(pendingName)
	if err != nil {
		return nil, nil, err
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(info, opened) || checkProtectedFile(opened, s.expectedUID()) != nil {
		_ = file.Close()
		return nil, nil, errors.Join(errors.New("installer journal pending handle changed"), statErr)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxJournalBytes+1))
	if readErr == nil {
		readErr = file.Sync()
	}
	openedAfter, afterStatErr := file.Stat()
	closeErr := file.Close()
	pathAfter, pathErr := s.root.Lstat(pendingName)
	if readErr != nil || afterStatErr != nil || closeErr != nil || pathErr != nil ||
		len(raw) != int(info.Size()) || !os.SameFile(info, openedAfter) || !os.SameFile(info, pathAfter) ||
		checkProtectedFile(openedAfter, s.expectedUID()) != nil || checkProtectedFile(pathAfter, s.expectedUID()) != nil {
		return nil, nil, errors.Join(errors.New("read stable installer journal pending slot"), readErr, afterStatErr, closeErr, pathErr)
	}
	if err := s.verifyRoot(ctx); err != nil {
		return nil, nil, err
	}
	return info, raw, nil
}
