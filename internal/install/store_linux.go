//go:build linux

package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

const (
	journalName      = "installation-transaction.json"
	pendingName      = "installation-transaction.pending"
	installDirectory = "install"
	intentFileName   = "intent.json"
	maxJournalBytes  = 16 << 10
	maxIntentBytes   = 256 << 10
)

type journalStoreConfig struct {
	testRoot      bool
	beforePublish func() error
	afterPublish  func() error
}

type journalSnapshot struct {
	present bool
	raw     []byte
	info    os.FileInfo
	tx      transaction
}

// journalStore borrows root. The caller keeps the existing privileged updater
// lock for its lifetime, so this store doesn't introduce a second lock.
type journalStore struct {
	mu        sync.Mutex
	root      *os.Root
	rootInfo  os.FileInfo
	config    journalStoreConfig
	snapshot  *journalSnapshot
	closed    bool
	uncertain bool
}

func newJournalStore(ctx context.Context, root *os.Root) (*journalStore, error) {
	return newJournalStoreWithConfig(ctx, root, journalStoreConfig{})
}

// newJournalStoreWithConfig is private so fixture roots and publication faults
// can be exercised without adding a host path override to the production API.
func newJournalStoreWithConfig(ctx context.Context, root *os.Root, config journalStoreConfig) (*journalStore, error) {
	if ctx == nil || root == nil {
		return nil, errors.New("installer journal root is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open installer journal store: %w", err)
	}
	info, err := root.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("inspect installer state root: %w", err)
	}
	s := &journalStore{root: root, rootInfo: info, config: config}
	if err := s.verifyRoot(ctx); err != nil {
		return nil, fmt.Errorf("validate installer state root: %w", err)
	}
	return s, nil
}

func (s *journalStore) Load(ctx context.Context) (*transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(ctx); err != nil {
		return nil, err
	}
	if _, err := s.root.Lstat(pendingName); err == nil {
		return nil, errors.New("installer journal publication is pending recovery")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("inspect installer journal pending slot: %w", err)
	}
	raw, info, present, err := s.readJournal(ctx)
	if err != nil {
		return nil, err
	}
	if !present {
		s.snapshot = &journalSnapshot{}
		return nil, nil
	}
	tx, err := decodeJournal(raw)
	if err != nil {
		return nil, fmt.Errorf("decode installer transaction journal: %w", err)
	}
	if err := validateTransaction(&tx, tx.Mode, tx.Phase); err != nil {
		return nil, fmt.Errorf("validate installer transaction journal: %w", err)
	}
	if _, err := s.readIntentLocked(ctx, tx); err != nil {
		return nil, fmt.Errorf("verify installer transaction intent: %w", err)
	}
	if err := s.verifyRoot(ctx); err != nil {
		return nil, fmt.Errorf("recheck installer state root after journal read: %w", err)
	}
	s.snapshot = &journalSnapshot{present: true, raw: bytes.Clone(raw), info: info, tx: *cloneTransaction(&tx)}
	return cloneTransaction(&tx), nil
}

func (s *journalStore) Record(ctx context.Context, prior *transaction, next transaction) (retErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(ctx); err != nil {
		return err
	}
	if err := validateTransaction(&next, next.Mode, next.Phase); err != nil {
		return fmt.Errorf("validate next installer transaction: %w", err)
	}
	if prior == nil {
		if next.Phase != phasePrepared {
			return errors.New("initial installer journal phase must be prepared")
		}
		if s.snapshot != nil && s.snapshot.present {
			return errors.New("installer journal disappeared after it was loaded")
		}
	} else if !sameImmutableTransaction(*prior, next) {
		return errors.New("installer transaction immutable fields changed")
	}
	if _, err := s.readIntentLocked(ctx, next); err != nil {
		return fmt.Errorf("verify next installer intent: %w", err)
	}
	currentRaw, currentInfo, currentPresent, err := s.readJournal(ctx)
	if err != nil {
		return err
	}
	if prior == nil {
		if currentPresent {
			return errors.New("installer transaction already exists")
		}
	} else {
		if s.snapshot == nil || !s.snapshot.present || !currentPresent {
			return errors.New("installer journal compare-and-swap has no loaded prior")
		}
		priorRaw, marshalErr := json.Marshal(prior)
		if marshalErr != nil || !bytes.Equal(priorRaw, s.snapshot.raw) || !bytes.Equal(currentRaw, s.snapshot.raw) || !os.SameFile(currentInfo, s.snapshot.info) {
			return errors.New("installer journal compare-and-swap preimage changed")
		}
	}
	if _, err := s.root.Lstat(pendingName); err == nil {
		return errors.New("installer journal pending slot is occupied")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect installer journal pending slot: %w", err)
	}
	raw, err := json.Marshal(next)
	if err != nil || len(raw) == 0 || len(raw) > maxJournalBytes {
		return errors.New("installer transaction journal encoding is invalid")
	}
	pending, err := s.root.OpenFile(pendingName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create installer journal pending slot: %w", err)
	}
	created, statErr := pending.Stat()
	if statErr != nil || checkProtectedFile(created, s.expectedUID()) != nil {
		_ = pending.Close()
		return errors.Join(errors.New("new installer journal pending slot is unsafe"), statErr)
	}
	published := false
	writeComplete := false
	defer func() {
		if published {
			return
		}
		if cleanupErr := s.removeOwnedPending(ctx, created, raw, writeComplete); cleanupErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("clean up installer journal pending slot: %w", cleanupErr))
		}
	}()
	writeErr := writeAll(pending, raw)
	writeComplete = writeErr == nil
	if writeErr == nil {
		writeErr = pending.Sync()
	}
	closeErr := pending.Close()
	if writeErr != nil || closeErr != nil {
		return fmt.Errorf("write installer journal pending slot: %w", errors.Join(writeErr, closeErr))
	}
	if err := syncDirectory(s.root); err != nil {
		return fmt.Errorf("sync installer journal pending slot: %w", err)
	}
	if err := s.verifyRoot(ctx); err != nil {
		return fmt.Errorf("recheck installer state root before publication: %w", err)
	}
	if err := s.checkPending(ctx, raw, created); err != nil {
		return err
	}
	currentRaw, currentInfo, currentPresent, err = s.readJournal(ctx)
	if err != nil {
		return err
	}
	if prior == nil {
		if currentPresent {
			return errors.New("installer journal appeared before publication")
		}
	} else if !currentPresent || !bytes.Equal(currentRaw, s.snapshot.raw) || !os.SameFile(currentInfo, s.snapshot.info) {
		return errors.New("installer journal changed before publication")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publish installer transaction journal: %w", err)
	}
	if s.config.beforePublish != nil {
		if err := s.config.beforePublish(); err != nil {
			return fmt.Errorf("installer journal prepublication fence: %w", err)
		}
	}
	if err := s.verifyRoot(ctx); err != nil {
		return fmt.Errorf("recheck installer state root at publication: %w", err)
	}
	if err := s.checkPending(ctx, raw, created); err != nil {
		return err
	}
	// Recheck the CAS immediately before rename. The caller's privileged lock
	// excludes cooperating writers; this catches stale handles and injected races.
	currentRaw, currentInfo, currentPresent, err = s.readJournal(ctx)
	if err != nil {
		return err
	}
	if prior == nil {
		if currentPresent {
			return errors.New("installer journal appeared before publication")
		}
	} else if !currentPresent || !bytes.Equal(currentRaw, s.snapshot.raw) || !os.SameFile(currentInfo, s.snapshot.info) {
		return errors.New("installer journal changed before publication")
	}
	if err := s.root.Rename(pendingName, journalName); err != nil {
		return fmt.Errorf("publish installer transaction journal: %w", err)
	}
	published = true
	s.uncertain = true
	if s.config.afterPublish != nil {
		if err := s.config.afterPublish(); err != nil {
			return fmt.Errorf("installer journal publication is uncertain: %w", err)
		}
	}
	if err := syncDirectory(s.root); err != nil {
		return fmt.Errorf("sync published installer transaction journal; publication is uncertain: %w", err)
	}
	if err := s.verifyRoot(ctx); err != nil {
		return fmt.Errorf("recheck installer state root after publication; publication is uncertain: %w", err)
	}
	publishedRaw, publishedInfo, present, err := s.readJournal(ctx)
	if err != nil || !present || !bytes.Equal(publishedRaw, raw) || !os.SameFile(publishedInfo, created) {
		return errors.Join(errors.New("installer journal publication is uncertain"), err)
	}
	s.snapshot = &journalSnapshot{present: true, raw: bytes.Clone(raw), info: publishedInfo, tx: *cloneTransaction(&next)}
	s.uncertain = false
	return nil
}

func (s *journalStore) CreateIntent(ctx context.Context, id string, body []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(ctx); err != nil {
		return "", err
	}
	if !lowerHex(id, 64) || len(body) == 0 || len(body) > maxIntentBytes {
		return "", errors.New("installer intent identity or size is invalid")
	}
	if err := s.verifyRoot(ctx); err != nil {
		return "", err
	}
	installInfo, err := s.openOrCreateDirectory(ctx, installDirectory, true)
	if err != nil {
		return "", fmt.Errorf("prepare installer intent directory: %w", err)
	}
	defer installInfo.root.Close()
	opInfo, err := s.openOrCreateChildDirectory(ctx, installInfo.root, id)
	if err != nil {
		return "", fmt.Errorf("prepare installer operation directory: %w", err)
	}
	defer opInfo.root.Close()
	if _, err := opInfo.root.Lstat(intentFileName); err == nil {
		return "", errors.New("installer intent already exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("inspect installer intent slot: %w", err)
	}
	file, err := opInfo.root.OpenFile(intentFileName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("create installer intent: %w", err)
	}
	info, statErr := file.Stat()
	if statErr != nil || checkProtectedFile(info, s.expectedUID()) != nil {
		_ = file.Close()
		return "", errors.Join(errors.New("new installer intent file is unsafe"), statErr)
	}
	err = writeAll(file, body)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return "", fmt.Errorf("write installer intent: %w", errors.Join(err, closeErr))
	}
	if err := syncDirectory(opInfo.root); err != nil {
		return "", fmt.Errorf("sync installer intent directory: %w", err)
	}
	if err := syncDirectory(installInfo.root); err != nil {
		return "", fmt.Errorf("sync installer operation parent: %w", err)
	}
	if err := s.verifyRoot(ctx); err != nil {
		return "", fmt.Errorf("recheck installer state root after intent creation: %w", err)
	}
	if err := verifyDirEntry(s.root, installDirectory, installInfo.info, s.expectedUID(), true); err != nil {
		return "", fmt.Errorf("recheck installer intent parent binding: %w", err)
	}
	if err := verifyDirEntry(installInfo.root, id, opInfo.info, s.expectedUID(), true); err != nil {
		return "", fmt.Errorf("recheck installer operation directory: %w", err)
	}
	if err := verifyDirEntry(opInfo.root, ".", opInfo.info, s.expectedUID(), true); err != nil {
		return "", fmt.Errorf("recheck installer operation root: %w", err)
	}
	if err := verifyFileEntry(opInfo.root, intentFileName, info, s.expectedUID(), maxIntentBytes); err != nil {
		return "", fmt.Errorf("recheck installer intent file: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func (s *journalStore) ReadIntent(ctx context.Context, tx transaction) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(ctx); err != nil {
		return nil, err
	}
	return s.readIntentLocked(ctx, tx)
}

func (s *journalStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

type pinnedDirectory struct {
	root *os.Root
	info os.FileInfo
}

func (s *journalStore) openOrCreateDirectory(ctx context.Context, name string, install bool) (pinnedDirectory, error) {
	info, err := s.root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		if err := ctx.Err(); err != nil {
			return pinnedDirectory{}, err
		}
		if err := s.root.Mkdir(name, 0700); err != nil {
			return pinnedDirectory{}, err
		}
		if err := syncDirectory(s.root); err != nil {
			return pinnedDirectory{}, errors.Join(errors.New("new installer directory remains after sync failure"), err)
		}
		info, err = s.root.Lstat(name)
	}
	if err != nil {
		return pinnedDirectory{}, err
	}
	if err := validateDirectory(info, s.expectedUID(), true); err != nil {
		return pinnedDirectory{}, err
	}
	child, err := s.root.OpenRoot(name)
	if err != nil {
		return pinnedDirectory{}, err
	}
	opened, err := child.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = child.Close()
		return pinnedDirectory{}, errors.Join(errors.New("installer directory identity changed"), err)
	}
	if install && name != installDirectory {
		_ = child.Close()
		return pinnedDirectory{}, errors.New("installer directory name is invalid")
	}
	return pinnedDirectory{root: child, info: info}, nil
}

func (s *journalStore) openOrCreateChildDirectory(ctx context.Context, parent *os.Root, id string) (pinnedDirectory, error) {
	info, err := parent.Lstat(id)
	if errors.Is(err, fs.ErrNotExist) {
		if err := ctx.Err(); err != nil {
			return pinnedDirectory{}, err
		}
		if err := parent.Mkdir(id, 0700); err != nil {
			return pinnedDirectory{}, err
		}
		if err := syncDirectory(parent); err != nil {
			return pinnedDirectory{}, errors.Join(errors.New("new installer operation directory remains after sync failure"), err)
		}
		info, err = parent.Lstat(id)
	}
	if err != nil {
		return pinnedDirectory{}, err
	}
	if err := validateDirectory(info, s.expectedUID(), true); err != nil {
		return pinnedDirectory{}, err
	}
	child, err := parent.OpenRoot(id)
	if err != nil {
		return pinnedDirectory{}, err
	}
	opened, err := child.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = child.Close()
		return pinnedDirectory{}, errors.Join(errors.New("installer operation directory identity changed"), err)
	}
	return pinnedDirectory{root: child, info: info}, nil
}

func (s *journalStore) readIntentLocked(ctx context.Context, tx transaction) ([]byte, error) {
	if !lowerHex(tx.ID, 64) || !lowerHex(tx.IntentSHA256, 64) {
		return nil, errors.New("installer intent identity is invalid")
	}
	if err := s.verifyRoot(ctx); err != nil {
		return nil, err
	}
	install, err := s.openExistingDirectory(installDirectory)
	if err != nil {
		return nil, fmt.Errorf("open installer intent parent: %w", err)
	}
	defer install.Close()
	installInfo, err := install.Stat(".")
	if err != nil || validateDirectory(installInfo, s.expectedUID(), true) != nil {
		return nil, errors.Join(errors.New("installer intent parent directory is unsafe"), err)
	}
	installPathInfo, err := s.root.Lstat(installDirectory)
	if err != nil || !os.SameFile(installInfo, installPathInfo) {
		return nil, errors.Join(errors.New("installer intent parent identity changed"), err)
	}
	op, err := install.OpenRoot(tx.ID)
	if err != nil {
		return nil, fmt.Errorf("open installer operation directory: %w", err)
	}
	defer op.Close()
	opInfo, err := op.Stat(".")
	if err != nil || validateDirectory(opInfo, s.expectedUID(), true) != nil {
		return nil, errors.Join(errors.New("installer operation directory is unsafe"), err)
	}
	pathInfo, err := install.Lstat(tx.ID)
	if err != nil || !os.SameFile(opInfo, pathInfo) {
		return nil, errors.Join(errors.New("installer operation directory identity changed"), err)
	}
	info, err := op.Lstat(intentFileName)
	if err != nil {
		return nil, fmt.Errorf("inspect installer intent: %w", err)
	}
	if err := checkProtectedFile(info, s.expectedUID()); err != nil || info.Size() > maxIntentBytes {
		return nil, errors.Join(errors.New("installer intent file is unsafe"), err)
	}
	file, err := op.Open(intentFileName)
	if err != nil {
		return nil, fmt.Errorf("open installer intent: %w", err)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || checkProtectedFile(opened, s.expectedUID()) != nil {
		_ = file.Close()
		return nil, errors.Join(errors.New("installer intent handle identity changed"), err)
	}
	body, readErr := io.ReadAll(io.LimitReader(file, maxIntentBytes+1))
	openedAfter, statErr := file.Stat()
	closeErr := file.Close()
	pathAfter, pathErr := op.Lstat(intentFileName)
	if readErr != nil || statErr != nil || closeErr != nil || pathErr != nil || len(body) == 0 || len(body) > maxIntentBytes || !os.SameFile(info, openedAfter) || !os.SameFile(info, pathAfter) || checkProtectedFile(openedAfter, s.expectedUID()) != nil || checkProtectedFile(pathAfter, s.expectedUID()) != nil {
		return nil, fmt.Errorf("read stable installer intent: %w", errors.Join(readErr, statErr, closeErr, pathErr))
	}
	pathInfoAfter, dirErr := install.Lstat(tx.ID)
	openedDirAfter, openedDirErr := op.Stat(".")
	if dirErr != nil || openedDirErr != nil || !os.SameFile(opInfo, pathInfoAfter) || !os.SameFile(opInfo, openedDirAfter) {
		return nil, errors.Join(errors.New("installer operation directory changed during intent read"), dirErr, openedDirErr)
	}
	installPathAfter, installPathErr := s.root.Lstat(installDirectory)
	installDirAfter, installDirErr := install.Stat(".")
	if installPathErr != nil || installDirErr != nil || !os.SameFile(installInfo, installPathAfter) || !os.SameFile(installInfo, installDirAfter) {
		return nil, errors.Join(errors.New("installer intent parent changed during read"), installPathErr, installDirErr)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != tx.IntentSHA256 {
		return nil, errors.New("installer intent digest mismatch")
	}
	if err := s.verifyRoot(ctx); err != nil {
		return nil, fmt.Errorf("recheck installer state root after intent read: %w", err)
	}
	return bytes.Clone(body), nil
}

func (s *journalStore) openExistingDirectory(name string) (*os.Root, error) {
	info, err := s.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if err := validateDirectory(info, s.expectedUID(), true); err != nil {
		return nil, err
	}
	child, err := s.root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := child.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = child.Close()
		return nil, errors.Join(errors.New("installer directory identity changed"), err)
	}
	return child, nil
}

func (s *journalStore) readJournal(ctx context.Context) ([]byte, os.FileInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, false, fmt.Errorf("read installer transaction journal: %w", err)
	}
	info, err := s.root.Lstat(journalName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("inspect installer transaction journal: %w", err)
	}
	if err := checkProtectedFile(info, s.expectedUID()); err != nil || info.Size() > maxJournalBytes {
		return nil, nil, false, errors.Join(errors.New("installer transaction journal file is unsafe"), err)
	}
	file, err := s.root.Open(journalName)
	if err != nil {
		return nil, nil, false, fmt.Errorf("open installer transaction journal: %w", err)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || checkProtectedFile(opened, s.expectedUID()) != nil {
		_ = file.Close()
		return nil, nil, false, errors.Join(errors.New("installer transaction journal handle changed"), err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxJournalBytes+1))
	openedAfter, statErr := file.Stat()
	closeErr := file.Close()
	pathAfter, pathErr := s.root.Lstat(journalName)
	if readErr != nil || statErr != nil || closeErr != nil || pathErr != nil || len(raw) == 0 || len(raw) > maxJournalBytes || !os.SameFile(info, openedAfter) || !os.SameFile(info, pathAfter) || checkProtectedFile(openedAfter, s.expectedUID()) != nil || checkProtectedFile(pathAfter, s.expectedUID()) != nil {
		return nil, nil, false, fmt.Errorf("read stable installer transaction journal: %w", errors.Join(readErr, statErr, closeErr, pathErr))
	}
	if err := s.verifyRoot(ctx); err != nil {
		return nil, nil, false, fmt.Errorf("recheck installer state root after journal read: %w", err)
	}
	return raw, info, true, nil
}

func (s *journalStore) checkPending(ctx context.Context, expected []byte, expectedInfo os.FileInfo) error {
	info, err := s.root.Lstat(pendingName)
	if err != nil {
		return fmt.Errorf("inspect installer journal pending slot: %w", err)
	}
	if !os.SameFile(info, expectedInfo) || checkProtectedFile(info, s.expectedUID()) != nil {
		return errors.New("installer journal pending slot was replaced")
	}
	file, err := s.root.Open(pendingName)
	if err != nil {
		return fmt.Errorf("open installer journal pending slot: %w", err)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return errors.Join(errors.New("installer journal pending handle changed"), err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxJournalBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(raw, expected) || checkProtectedFile(info, s.expectedUID()) != nil {
		return fmt.Errorf("installer journal pending content changed: %w", errors.Join(readErr, closeErr))
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("verify installer journal pending slot: %w", err)
	}
	return nil
}

// removeOwnedPending only removes the inode created by this Record call. It
// uses a short context detached from caller cancellation so cleanup can finish
// after an interrupted prepublication attempt. Any replacement is preserved.
func (s *journalStore) removeOwnedPending(parent context.Context, expectedInfo os.FileInfo, expected []byte, requireContent bool) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
	defer cancel()
	if err := s.verifyRoot(cleanupCtx); err != nil {
		return err
	}
	info, err := s.root.Lstat(pendingName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(info, expectedInfo) || checkProtectedFile(info, s.expectedUID()) != nil {
		return errors.New("pending entry changed; preserved")
	}
	file, err := s.root.Open(pendingName)
	if err != nil {
		return err
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(info, opened) || checkProtectedFile(opened, s.expectedUID()) != nil {
		_ = file.Close()
		return errors.Join(errors.New("pending handle changed; preserved"), statErr)
	}
	body, readErr := io.ReadAll(io.LimitReader(file, maxJournalBytes+1))
	openedAfter, afterStatErr := file.Stat()
	closeErr := file.Close()
	pathAfter, pathErr := s.root.Lstat(pendingName)
	if readErr != nil || afterStatErr != nil || closeErr != nil || pathErr != nil || !os.SameFile(info, openedAfter) || !os.SameFile(info, pathAfter) || checkProtectedFile(openedAfter, s.expectedUID()) != nil || checkProtectedFile(pathAfter, s.expectedUID()) != nil {
		return errors.Join(errors.New("pending entry changed during cleanup; preserved"), readErr, afterStatErr, closeErr, pathErr)
	}
	if requireContent && !bytes.Equal(body, expected) {
		return errors.New("pending contents changed; preserved")
	}
	if err := cleanupCtx.Err(); err != nil {
		return err
	}
	if err := s.verifyRoot(cleanupCtx); err != nil {
		return err
	}
	latest, err := s.root.Lstat(pendingName)
	if err != nil || !os.SameFile(info, latest) || checkProtectedFile(latest, s.expectedUID()) != nil {
		return errors.Join(errors.New("pending entry changed before cleanup; preserved"), err)
	}
	if err := s.root.Remove(pendingName); err != nil {
		return err
	}
	if err := syncDirectory(s.root); err != nil {
		return fmt.Errorf("sync removed pending entry: %w", err)
	}
	return nil
}

func (s *journalStore) verifyRoot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.root == nil {
		return errors.New("installer state root is unavailable")
	}
	info, err := s.root.Stat(".")
	if err != nil || !os.SameFile(s.rootInfo, info) {
		return errors.Join(errors.New("installer state root identity changed"), err)
	}
	if err := validateDirectory(info, s.expectedUID(), false); err != nil {
		return err
	}
	path := s.root.Name()
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("installer state root path is not canonical")
	}
	current, err := safepath.OpenRoot(ctx, path)
	if err != nil {
		return fmt.Errorf("reopen installer state root: %w", err)
	}
	defer current.Close()
	opened, err := current.Stat(".")
	if err != nil || !os.SameFile(s.rootInfo, opened) {
		return errors.Join(errors.New("installer state root path binding changed"), err)
	}
	return nil
}

func (s *journalStore) usable(ctx context.Context) error {
	if ctx == nil {
		return errors.New("installer journal context is unavailable")
	}
	if s.closed {
		return errors.New("installer journal store is closed")
	}
	if s.uncertain {
		return errors.New("installer journal publication is uncertain; reopen and inspect before mutation")
	}
	return s.verifyRoot(ctx)
}

func (s *journalStore) expectedUID() uint32 {
	if s.config.testRoot {
		return uint32(os.Geteuid())
	}
	return 0
}

func decodeJournal(raw []byte) (transaction, error) {
	if len(raw) == 0 || len(raw) > maxJournalBytes {
		return transaction{}, errors.New("installer journal size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var tx transaction
	if err := decoder.Decode(&tx); err != nil {
		return transaction{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return transaction{}, errors.New("installer journal has trailing data")
	}
	canonical, err := json.Marshal(tx)
	if err != nil || !bytes.Equal(raw, canonical) {
		return transaction{}, errors.New("installer journal encoding is not canonical")
	}
	return tx, nil
}

func sameImmutableTransaction(a, b transaction) bool {
	if a.Format != b.Format || a.ID != b.ID || a.Mode != b.Mode || a.IntentSHA256 != b.IntentSHA256 {
		return false
	}
	return sameOptionalImage(a.Prior, b.Prior) && sameOptionalImage(a.Target, b.Target)
}

func sameOptionalImage(a, b *Image) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func validateDirectory(info os.FileInfo, uid uint32, private bool) error {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 || private && info.Mode().Perm()&0077 != 0 {
		return errors.New("installer directory permissions are unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid {
		return errors.New("installer directory owner is unsafe")
	}
	return nil
}

func checkProtectedFile(info os.FileInfo, uid uint32) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return errors.New("installer file permissions are unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid || stat.Nlink != 1 {
		return errors.New("installer file owner or link count is unsafe")
	}
	return nil
}

func verifyDirEntry(root *os.Root, name string, expected os.FileInfo, uid uint32, private bool) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if err := validateDirectory(info, uid, private); err != nil || !os.SameFile(expected, info) {
		return errors.Join(errors.New("installer directory identity changed"), err)
	}
	return nil
}

func verifyFileEntry(root *os.Root, name string, expected os.FileInfo, uid uint32, limit int64) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if err := checkProtectedFile(info, uid); err != nil || info.Size() > limit || !os.SameFile(expected, info) {
		return errors.Join(errors.New("installer file identity changed"), err)
	}
	return nil
}

func syncDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func writeAll(file *os.File, raw []byte) error {
	for len(raw) > 0 {
		n, err := file.Write(raw)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		raw = raw[n:]
	}
	return nil
}
