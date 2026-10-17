//go:build linux

package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// reconcileLinuxAssetEffect only classifies and clears scratch left by one
// durable native-effect record. It never changes the public asset binding.
func reconcileLinuxAssetEffect(ctx context.Context, operationID string, role linuxAssetRole, before, staged linuxAssetState) (linuxAssetState, error) {
	if ctx == nil || ctx.Err() != nil || !validLinuxAssetOperationID(operationID) {
		if ctx != nil && ctx.Err() != nil {
			return linuxAssetState{}, ctx.Err()
		}
		return linuxAssetState{}, errors.New("invalid Linux asset effect reconciliation arguments")
	}
	spec, ok := linuxAssetSpecForRole(role)
	if !ok || !validLinuxAssetRecordedState(role, before) || !validLinuxAssetRecordedState(role, staged) {
		return linuxAssetState{}, errors.New("invalid recorded Linux asset effect state")
	}
	if staged.Present && before.Present && staged == before {
		return linuxAssetState{}, errors.New("recorded Linux asset before and staged identities are equal")
	}
	publication := staged.Present
	snapshot, err := inspectLinuxAsset(ctx, role)
	if err != nil {
		return linuxAssetState{}, err
	}
	defer snapshot.Close()
	if !snapshot.parentComplete {
		if !publication && !before.Present && !snapshot.state.Present {
			return linuxAssetState{}, nil
		}
		return linuxAssetState{}, errors.New("Linux asset effect parent is not provisioned")
	}
	if publication {
		return reconcileLinuxAssetPublication(ctx, snapshot, spec, operationID, before, staged)
	}
	return reconcileLinuxAssetRemoval(ctx, snapshot, spec, before)
}

func validLinuxAssetRecordedState(role linuxAssetRole, state linuxAssetState) bool {
	if !state.Present {
		return state == (linuxAssetState{})
	}
	spec, ok := linuxAssetSpecForRole(role)
	if !ok || state.Size < 0 || state.Size > linuxAssetLimit(role) || state.Mode != spec.Mode || state.Inode == 0 || len(state.SHA256) != sha256.Size*2 {
		return false
	}
	digest, err := hex.DecodeString(state.SHA256)
	return err == nil && len(digest) == sha256.Size && strings.ToLower(state.SHA256) == state.SHA256
}

func reconcileLinuxAssetPublication(ctx context.Context, public *linuxAssetSnapshot, spec linuxAssetSpec, operationID string, before, staged linuxAssetState) (linuxAssetState, error) {
	name := filepath.Base(public.path)
	pending := name + ".install-" + operationID
	pendingState, pendingFile, err := inspectLinuxAssetScratch(ctx, public, pending, spec)
	if err != nil {
		return linuxAssetState{}, err
	}
	if pendingFile != nil {
		defer pendingFile.Close()
	}
	publicState := public.state
	removePending := false
	switch {
	case !pendingState.Present && publicState == before:
		if !before.Present && staged.Present {
			return linuxAssetState{}, errors.New("Linux asset publication has neither public nor staged file")
		}
	case !pendingState.Present && publicState == staged:
		// Rename completed and the exact staged inode is public.
	case pendingState == staged && publicState == before:
		removePending = true
	case before.Present && pendingState == before && publicState == staged:
		removePending = true
	default:
		return linuxAssetState{}, errors.New("Linux asset publication state is ambiguous")
	}
	if pendingState.Present && !removePending {
		return linuxAssetState{}, errors.New("Linux asset pending file is not a recognized effect scratch file")
	}
	if removePending {
		if err := removeExactLinuxAssetScratch(ctx, public, pending, pendingFile, pendingState, spec); err != nil {
			return linuxAssetState{}, err
		}
	}
	if err := verifyLinuxAssetPublicUnchanged(ctx, public, publicState); err != nil {
		return linuxAssetState{}, err
	}
	return publicState, nil
}

func reconcileLinuxAssetRemoval(ctx context.Context, public *linuxAssetSnapshot, spec linuxAssetSpec, before linuxAssetState) (linuxAssetState, error) {
	name := filepath.Base(public.path)
	quarantine := fmt.Sprintf("%s.remove-%x-%x", name, before.Device, before.Inode)
	quarantineState, quarantineFile, err := inspectLinuxAssetScratch(ctx, public, quarantine, spec)
	if err != nil {
		return linuxAssetState{}, err
	}
	if quarantineFile != nil {
		defer quarantineFile.Close()
	}
	publicState := public.state
	if !before.Present {
		if publicState.Present || quarantineState.Present {
			return linuxAssetState{}, errors.New("absent Linux asset removal has unexpected public or quarantine file")
		}
		return linuxAssetState{}, nil
	}
	switch {
	case publicState == before && !quarantineState.Present:
		return publicState, nil
	case !publicState.Present && quarantineState == before:
		if err := removeExactLinuxAssetScratch(ctx, public, quarantine, quarantineFile, before, spec); err != nil {
			return linuxAssetState{}, err
		}
	case !publicState.Present && !quarantineState.Present:
		return linuxAssetState{}, nil
	default:
		return linuxAssetState{}, errors.New("Linux asset removal state is ambiguous")
	}
	if err := verifyLinuxAssetPublicUnchanged(ctx, public, publicState); err != nil {
		return linuxAssetState{}, err
	}
	return publicState, nil
}

func inspectLinuxAssetScratch(ctx context.Context, public *linuxAssetSnapshot, name string, spec linuxAssetSpec) (linuxAssetState, *os.File, error) {
	info, err := public.parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return linuxAssetState{}, nil, nil
	}
	if err != nil {
		return linuxAssetState{}, nil, fmt.Errorf("inspect fixed Linux asset scratch: %w", err)
	}
	file, state, err := inspectLinuxAssetFile(ctx, public.parent, name, info, spec)
	if err != nil {
		return linuxAssetState{}, nil, err
	}
	return state, file, nil
}

func removeExactLinuxAssetScratch(ctx context.Context, public *linuxAssetSnapshot, name string, pinned *os.File, expected linuxAssetState, spec linuxAssetSpec) error {
	if ctx == nil || ctx.Err() != nil {
		if ctx != nil {
			return ctx.Err()
		}
		return errors.New("nil Linux asset scratch cleanup context")
	}
	if pinned == nil || !expected.Present {
		return errors.New("Linux asset scratch identity is unavailable")
	}
	if err := verifyLinuxAssetPublicUnchanged(ctx, public, public.state); err != nil {
		return err
	}
	info, err := public.parent.Lstat(name)
	if err != nil {
		return fmt.Errorf("recheck fixed Linux asset scratch: %w", err)
	}
	pinnedInfo, err := pinned.Stat()
	if err != nil || !os.SameFile(info, pinnedInfo) {
		return errors.New("Linux asset scratch inode changed before cleanup")
	}
	currentFile, currentState, err := inspectLinuxAssetFile(ctx, public.parent, name, info, spec)
	if currentFile != nil {
		defer currentFile.Close()
	}
	if err != nil || currentState != expected {
		return errors.New("Linux asset scratch state changed before cleanup")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifyLinuxAssetPublicUnchanged(ctx, public, public.state); err != nil {
		return err
	}
	if err := public.parent.Remove(name); err != nil {
		return fmt.Errorf("remove recognized Linux asset scratch: %w", err)
	}
	if err := syncLinuxAssetDir(public.parent); err != nil {
		return fmt.Errorf("sync Linux asset parent after scratch cleanup: %w", err)
	}
	return verifyLinuxAssetPublicUnchanged(ctx, public, public.state)
}

func verifyLinuxAssetPublicUnchanged(ctx context.Context, public *linuxAssetSnapshot, expected linuxAssetState) error {
	if err := checkLinuxAssetSnapshot(ctx, public); err != nil {
		return err
	}
	if public.state != expected {
		return errors.New("public Linux asset state differs from effect classification")
	}
	return nil
}
