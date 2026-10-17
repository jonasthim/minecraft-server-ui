package managerstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
)

func readOptionalFormatOneHold(ctx context.Context, root *os.Root) (*maintenancePin, migrationHold, error) {
	if err := requireAbsent(root, "hold.pending"); err != nil {
		return nil, migrationHold{}, err
	}
	if _, err := root.Lstat("hold.json"); errors.Is(err, os.ErrNotExist) {
		return nil, migrationHold{}, nil
	} else if err != nil {
		return nil, migrationHold{}, err
	}
	return openCurrentHold(ctx, root)
}

func currentMaintenanceHoldFormat(ctx context.Context, root *os.Root) (int, error) {
	file, err := root.Open("hold.json")
	if err != nil {
		return 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > holdLimit || checkOpenFile(file, info) != nil {
		return 0, errors.New("maintenance hold is unsafe")
	}
	body, err := readMaintenanceFile(ctx, file, holdLimit)
	if err != nil {
		return 0, err
	}
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return 0, err
	}
	var header struct {
		Format int `json:"format"`
	}
	if err := json.Unmarshal(body, &header); err != nil || (header.Format != 1 && header.Format != restoreFormat) {
		return 0, errors.New("maintenance hold format is unknown")
	}
	return header.Format, nil
}

func captureRestoreFiles(ctx context.Context, root *os.Root, sources []sourceFile, records []fileRecord) ([]restoreFile, map[string]*os.File, error) {
	if len(sources) != len(records) {
		return nil, nil, errors.New("manager database file set changed")
	}
	files := make([]restoreFile, 0, len(sources))
	pins := make(map[string]*os.File, len(sources))
	for index, source := range sources {
		if err := ctx.Err(); err != nil {
			closeFiles(pins)
			return nil, nil, err
		}
		if source.name != records[index].Name {
			closeFiles(pins)
			return nil, nil, errors.New("manager database file set changed")
		}
		file, err := root.Open(source.name)
		if err != nil {
			closeFiles(pins)
			return nil, nil, err
		}
		info, err := file.Stat()
		if err != nil || info.Size() != records[index].Size || !sameFileState(source.info, info) || checkOpenFile(file, info) != nil {
			_ = file.Close()
			closeFiles(pins)
			return nil, nil, errors.New("manager database file identity changed")
		}
		identity, err := restoreNativeIdentity(file)
		if err != nil {
			_ = file.Close()
			closeFiles(pins)
			return nil, nil, err
		}
		files = append(files, restoreFile{Name: source.name, Size: records[index].Size, SHA256: records[index].SHA256, Identity: identity})
		pins[source.name] = file
	}
	return files, pins, nil
}

func closeFiles(files map[string]*os.File) error {
	var result error
	for name, file := range files {
		if file != nil {
			result = errors.Join(result, file.Close())
			delete(files, name)
		}
	}
	return result
}

func recordsWithNativeIdentity(root *os.Root, owned ownedFiles, records []fileRecord) ([]restoreFile, error) {
	files := make([]restoreFile, 0, len(records))
	for _, record := range records {
		ownedFile, ok := owned[record.Name]
		if !ok || ownedFile.file == nil {
			return nil, errors.New("staged file handle is unavailable")
		}
		if err := checkOrdinaryFile(root, record.Name, ownedFile.info); err != nil {
			return nil, err
		}
		identity, err := restoreNativeIdentity(ownedFile.file)
		if err != nil {
			return nil, err
		}
		files = append(files, restoreFile{Name: record.Name, Size: record.Size, SHA256: record.SHA256, Identity: identity})
	}
	return files, nil
}

func writeExclusiveFile(ctx context.Context, root *os.Root, name string, body []byte) error {
	if len(body) == 0 || int64(len(body)) > restoreIntentLimit {
		return errors.New("private maintenance file exceeds bound")
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || checkOpenFile(file, info) != nil {
		return errors.New("private maintenance file is unsafe")
	}
	for offset := 0; offset < len(body); {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, writeErr := file.Write(body[offset:])
		if writeErr != nil || n <= 0 || n > len(body)-offset {
			return errors.New("private maintenance file write was incomplete")
		}
		offset += n
	}
	if err := file.Sync(); err != nil {
		return errors.New("private maintenance file sync failed")
	}
	if err := checkOrdinaryFile(root, name, info); err != nil {
		return err
	}
	return nil
}

func publishRestoreHold(ctx context.Context, session *lockedMaintenance, prior *maintenancePin, hold restoreHold, fence func(context.Context, *os.Root, *os.Root) error) (*maintenancePin, error) {
	body, err := jsonMarshalRestoreHold(hold)
	if err != nil {
		return nil, err
	}
	if prior == nil {
		if err := ensureNoHold(session.maintenance); err != nil {
			return nil, err
		}
		pin, err := createHoldPending(ctx, session.maintenance, body)
		if err != nil {
			return nil, err
		}
		if err := session.maintenance.Rename("hold.pending", "hold.json"); err != nil {
			_ = pin.file.Close()
			return nil, err
		}
		pin.raw = append([]byte(nil), body...)
		if err := verifyHoldPin(ctx, session.maintenance, "hold.json", pin); err != nil {
			_ = pin.file.Close()
			return nil, err
		}
		if err := fence(ctx, session.data, session.maintenance); err != nil {
			_ = pin.file.Close()
			return nil, err
		}
		return pin, nil
	}
	return replaceRawHoldCAS(ctx, session, prior, body, fence)
}

func replaceRestoreHold(ctx context.Context, session *lockedMaintenance, prior *maintenancePin, hold restoreHold, fence func(context.Context, *os.Root, *os.Root) error) (*maintenancePin, error) {
	body, err := jsonMarshalRestoreHold(hold)
	if err != nil {
		return nil, err
	}
	return replaceRawHoldCAS(ctx, session, prior, body, fence)
}

func replaceRawHoldCAS(ctx context.Context, session *lockedMaintenance, prior *maintenancePin, body []byte, fence func(context.Context, *os.Root, *os.Root) error) (*maintenancePin, error) {
	if err := verifyHoldPin(ctx, session.maintenance, "hold.json", prior); err != nil {
		return nil, err
	}
	pending, err := createHoldPending(ctx, session.maintenance, body)
	if err != nil {
		return nil, err
	}
	if err := verifyHoldPin(ctx, session.maintenance, "hold.json", prior); err != nil {
		_ = pending.file.Close()
		return nil, err
	}
	if err := verifyHoldPin(ctx, session.maintenance, "hold.pending", pending); err != nil {
		_ = pending.file.Close()
		return nil, err
	}
	if err := session.maintenance.Rename("hold.pending", "hold.json"); err != nil {
		_ = pending.file.Close()
		return nil, err
	}
	if err := verifyHoldPin(ctx, session.maintenance, "hold.json", pending); err != nil {
		_ = pending.file.Close()
		return nil, err
	}
	if err := fence(ctx, session.data, session.maintenance); err != nil {
		_ = pending.file.Close()
		return nil, err
	}
	return pending, nil
}

func jsonMarshalRestoreHold(hold restoreHold) ([]byte, error) {
	if !validateRestoreHold(hold) {
		return nil, errors.New("restore hold fields are invalid")
	}
	body, err := json.Marshal(hold)
	if err != nil || len(body) == 0 || len(body) > holdLimit {
		return nil, errors.New("restore hold exceeds bound")
	}
	return body, nil
}

func applyRestoreMoves(ctx context.Context, session *lockedMaintenance, operationID string, operation, stage, preimage *os.Root, intent restoreIntent, intentHash string, holdPin *maintenancePin, deps restoreDeps) error {
	if err := verifyRestoreLocations(ctx, session, operation, operationID, stage, preimage, intent, true); err != nil {
		return err
	}
	for index, expected := range intent.OldFiles {
		if err := ctx.Err(); err != nil {
			return err
		}
		if fileMatches(ctx, preimage, expected) {
			continue
		}
		if err := verifyRestoreRoots(session, operationID, operation, stage, preimage); err != nil {
			return err
		}
		if err := verifyImmutableRestoreState(ctx, session, operation, holdPin, intent, intentHash); err != nil {
			return err
		}
		if err := verifyRestoreFileAt(ctx, session.data, expected); err != nil {
			return err
		}
		if err := session.data.Rename(expected.Name, filepath.ToSlash(filepath.Join("maintenance", operationID, "preimage", expected.Name))); err != nil {
			return err
		}
		if err := verifyRestoreFileAt(ctx, preimage, expected); err != nil {
			return err
		}
		if err := fenceRestoreMove(ctx, deps.fence, deps.syncDirectory, session, operationID, operation, stage, preimage, preimage, session.data); err != nil {
			return err
		}
		if err := verifyImmutableRestoreState(ctx, session, operation, holdPin, intent, intentHash); err != nil {
			return err
		}
		if deps.afterOldMove != nil {
			if err := deps.afterOldMove(index); err != nil {
				return err
			}
		}
	}
	for _, expected := range intent.OldFiles {
		if _, err := session.data.Lstat(expected.Name); err == nil {
			targetPlaced := false
			for _, target := range intent.TargetFiles {
				if target.Name == expected.Name && fileMatches(ctx, session.data, target) {
					targetPlaced = true
					break
				}
			}
			if !targetPlaced {
				return errors.New("old database file remained beside target set")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for index, expected := range intent.TargetFiles {
		if err := ctx.Err(); err != nil {
			return err
		}
		if fileMatches(ctx, session.data, expected) {
			continue
		}
		if err := verifyRestoreRoots(session, operationID, operation, stage, preimage); err != nil {
			return err
		}
		if err := verifyImmutableRestoreState(ctx, session, operation, holdPin, intent, intentHash); err != nil {
			return err
		}
		if err := verifyRestoreFileAt(ctx, stage, expected); err != nil {
			return err
		}
		if _, err := session.data.Lstat(expected.Name); err == nil || !errors.Is(err, os.ErrNotExist) {
			return errors.New("target database path is occupied")
		}
		if err := session.data.Rename(filepath.ToSlash(filepath.Join("maintenance", operationID, "stage", expected.Name)), expected.Name); err != nil {
			return err
		}
		if err := verifyRestoreFileAt(ctx, session.data, expected); err != nil {
			return err
		}
		if err := fenceRestoreMove(ctx, deps.fence, deps.syncDirectory, session, operationID, operation, stage, preimage, session.data, stage); err != nil {
			return err
		}
		if err := verifyImmutableRestoreState(ctx, session, operation, holdPin, intent, intentHash); err != nil {
			return err
		}
		if deps.afterTargetMove != nil {
			if err := deps.afterTargetMove(index); err != nil {
				return err
			}
		}
	}
	return verifyRestoreLocations(ctx, session, operation, operationID, stage, preimage, intent, false)
}

func verifyImmutableRestoreState(ctx context.Context, session *lockedMaintenance, operation *os.Root, holdPin *maintenancePin, intent restoreIntent, intentHash string) error {
	if err := verifyHoldPin(ctx, session.maintenance, "hold.json", holdPin); err != nil {
		return errors.New("restore hold changed")
	}
	current, err := readRestoreIntent(ctx, operation, intentHash)
	if err != nil {
		return errors.New("restore intent changed")
	}
	want, _, err := marshalRestoreIntent(intent)
	if err != nil {
		return err
	}
	got, _, err := marshalRestoreIntent(current)
	if err != nil || !bytes.Equal(want, got) {
		return errors.New("restore intent changed")
	}
	return ctx.Err()
}

func verifyRestoreLocations(ctx context.Context, session *lockedMaintenance, operation *os.Root, operationID string, stage, preimage *os.Root, intent restoreIntent, prepared bool) error {
	if err := verifyRestoreRoots(session, operationID, operation, stage, preimage); err != nil {
		return err
	}
	if err := verifyRestoreLayout(ctx, operation, stage, preimage, intent); err != nil {
		return err
	}
	if err := verifyLiveSQLiteEntrySet(ctx, session.data, intent); err != nil {
		return err
	}
	oldAllPreimage := true
	for _, expected := range intent.OldFiles {
		live := fileMatches(ctx, session.data, expected)
		if err := ctx.Err(); err != nil {
			return err
		}
		old := fileMatches(ctx, preimage, expected)
		if err := ctx.Err(); err != nil {
			return err
		}
		if live == old {
			return errors.New("old file location is ambiguous")
		}
		if live {
			oldAllPreimage = false
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	anyTargetLive := false
	for _, expected := range intent.TargetFiles {
		staged := fileMatches(ctx, stage, expected)
		if err := ctx.Err(); err != nil {
			return err
		}
		live := fileMatches(ctx, session.data, expected)
		if err := ctx.Err(); err != nil {
			return err
		}
		if staged == live {
			return errors.New("target file location is ambiguous")
		}
		if live {
			anyTargetLive = true
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if anyTargetLive && !oldAllPreimage {
		return errors.New("target files appeared before all preimages were preserved")
	}
	return nil
}

func verifyRestoreRoots(session *lockedMaintenance, operationID string, operation, stage, preimage *os.Root) error {
	if session == nil || operation == nil || stage == nil || preimage == nil {
		return errors.New("restore roots are unavailable")
	}
	operationInfo, err := session.maintenance.Lstat(operationID)
	operationOpened, openErr := operation.Stat(".")
	operationPath, pathErr := os.Lstat(filepath.Join(session.maintenance.Name(), operationID))
	if err != nil || openErr != nil || pathErr != nil || !os.SameFile(operationInfo, operationOpened) || !os.SameFile(operationInfo, operationPath) || checkPrivateDirectory(session.maintenance, operationID, operationInfo, session.managerSID) != nil {
		return errors.New("restore operation directory binding changed")
	}
	for _, item := range []struct {
		name string
		root *os.Root
	}{{"stage", stage}, {"preimage", preimage}} {
		info, err := operation.Lstat(item.name)
		opened, openErr := item.root.Stat(".")
		pathInfo, pathErr := os.Lstat(filepath.Join(operation.Name(), item.name))
		if err != nil || openErr != nil || pathErr != nil || !os.SameFile(info, opened) || !os.SameFile(info, pathInfo) || checkPrivateDirectory(operation, item.name, info, session.managerSID) != nil {
			return errors.New("restore nested directory binding changed")
		}
	}
	return nil
}

func verifyLiveSQLiteEntrySet(ctx context.Context, root *os.Root, intent restoreIntent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := sourceFilesAllowMissingDatabase(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		matched := false
		for _, record := range intent.OldFiles {
			if record.Name == entry.name && fileMatches(ctx, root, record) {
				matched = true
				break
			}
		}
		if !matched {
			for _, record := range intent.TargetFiles {
				if record.Name == entry.name && fileMatches(ctx, root, record) {
					matched = true
					break
				}
			}
		}
		if !matched {
			return errors.New("manager database directory contains an unexpected SQLite file")
		}
	}
	return ctx.Err()
}

func verifyRestoreLayout(ctx context.Context, operation, stage, preimage *os.Root, intent restoreIntent) error {
	if operation == nil || stage == nil || preimage == nil {
		return errors.New("restore operation roots are unavailable")
	}
	operationEntries, err := readBoundedRestoreDir(operation, 3)
	if err != nil || len(operationEntries) != 3 {
		return errors.New("restore operation contains unexpected entries")
	}
	operationNames := []string{operationEntries[0].Name(), operationEntries[1].Name(), operationEntries[2].Name()}
	sort.Strings(operationNames)
	if !reflect.DeepEqual(operationNames, []string{"intent.json", "preimage", "stage"}) {
		return errors.New("restore operation contains unexpected entries")
	}
	stageExpected := make([]string, 0, len(intent.TargetFiles))
	for _, file := range intent.TargetFiles {
		if err := ctx.Err(); err != nil {
			return err
		}
		if fileMatches(ctx, stage, file) {
			stageExpected = append(stageExpected, file.Name)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	preimageExpected := make([]string, 0, len(intent.OldFiles)+1)
	for _, file := range intent.OldFiles {
		if err := ctx.Err(); err != nil {
			return err
		}
		if fileMatches(ctx, preimage, file) {
			preimageExpected = append(preimageExpected, file.Name)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if intent.PriorHoldSHA256 != "" {
		prior, err := preimage.Open("prior-hold.json")
		if err != nil {
			return errors.New("prior maintenance hold preimage is missing")
		}
		info, statErr := prior.Stat()
		if statErr != nil || checkOpenFile(prior, info) != nil || checkOrdinaryFile(preimage, "prior-hold.json", info) != nil {
			_ = prior.Close()
			return errors.New("prior maintenance hold preimage is unsafe")
		}
		body, readErr := readMaintenanceFile(ctx, prior, holdLimit)
		closeErr := prior.Close()
		if readErr != nil || closeErr != nil || len(body) > holdLimit || restoreHashForBytes(body) != intent.PriorHoldSHA256 {
			return errors.New("prior maintenance hold preimage changed")
		}
		preimageExpected = append(preimageExpected, "prior-hold.json")
	}
	sort.Strings(stageExpected)
	sort.Strings(preimageExpected)
	if err := exactRootEntries(stage, stageExpected); err != nil {
		return errors.New("restore staging directory contains unexpected entries")
	}
	if err := exactRootEntries(preimage, preimageExpected); err != nil {
		return errors.New("restore preimage directory contains unexpected entries")
	}
	return nil
}

func exactRootEntries(root *os.Root, expected []string) error {
	entries, err := readBoundedRestoreDir(root, len(expected))
	if err != nil || len(entries) != len(expected) {
		return errors.New("restore directory entries changed")
	}
	actual := make([]string, len(entries))
	for index, entry := range entries {
		actual[index] = entry.Name()
	}
	sort.Strings(actual)
	for index, name := range expected {
		if actual[index] != name {
			return errors.New("restore directory entries changed")
		}
	}
	return nil
}

func readBoundedRestoreDir(root *os.Root, maximum int) ([]os.DirEntry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(maximum + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maximum {
		return nil, errors.New("restore directory contains too many entries")
	}
	return entries, nil
}

func fileMatches(ctx context.Context, root *os.Root, expected restoreFile) bool {
	return verifyRestoreFileAt(ctx, root, expected) == nil
}

func verifyRestoreFileAt(ctx context.Context, root *os.Root, expected restoreFile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := root.Open(expected.Name)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() != expected.Size || checkOpenFile(file, info) != nil {
		return errors.New("restore file is unsafe")
	}
	identity, err := restoreNativeIdentity(file)
	if err != nil || identity != expected.Identity {
		return errors.New("restore file identity changed")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(&restoreContextReader{ctx: ctx, reader: file}, expected.Size+1)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return errors.New("restore file bytes changed")
	}
	pathInfo, err := root.Lstat(expected.Name)
	if err != nil || !os.SameFile(info, pathInfo) || checkOrdinaryFile(root, expected.Name, pathInfo) != nil {
		return errors.New("restore file path changed")
	}
	return ctx.Err()
}

func validateRestoredSet(ctx context.Context, session *lockedMaintenance, operationID string, operation *os.Root, intent restoreIntent, afterValidation func() error) error {
	for _, expected := range intent.TargetFiles {
		if err := verifyRestoreFileAt(ctx, session.data, expected); err != nil {
			return err
		}
	}
	validationName := "restore-validation-" + operationID
	validation, validationName, validationInfo, err := createScratch(session.maintenance, session.managerSID)
	if err != nil {
		return err
	}
	defer validation.Close()
	validationFiles, err := copySnapshotForValidation(ctx, session.data, validation, toFileRecords(intent.TargetFiles))
	if err != nil {
		return err
	}
	defer closeOwnedFiles(validationFiles)
	path := filepath.Join(session.dataDir, "maintenance", validationName, "manager.db")
	schema, err := validateSQLiteBound(ctx, path, func() error {
		return verifyValidationBinding(ctx, session.dataDir, session.data, session.maintenance, validationName, validation, validationInfo, validationFiles)
	}, nil)
	if err != nil || schema != intent.TargetSchema {
		return errors.New("restored database failed schema or integrity validation")
	}
	if err := captureSQLiteSidecars(validation, validationFiles); err != nil {
		return err
	}
	if afterValidation != nil {
		if err := afterValidation(); err != nil {
			return err
		}
	}
	if err := removeOwnedTree(session.maintenance, validationName, validationInfo, validation, validationFiles); err != nil {
		return errors.New("restore validation cleanup was incomplete")
	}
	for _, expected := range intent.TargetFiles {
		if err := verifyRestoreFileAt(ctx, session.data, expected); err != nil {
			return errors.New("live restored manager database changed during validation")
		}
	}
	if err := verifyLiveSQLiteEntrySet(ctx, session.data, intent); err != nil {
		return errors.New("live restored manager database file set changed during validation")
	}
	return ctx.Err()
}

type restoreContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *restoreContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err == nil {
		if contextErr := r.ctx.Err(); contextErr != nil {
			return n, contextErr
		}
	}
	return n, err
}

func toFileRecords(files []restoreFile) []fileRecord {
	out := make([]fileRecord, 0, len(files))
	for _, file := range files {
		out = append(out, fileRecord{Name: file.Name, Size: file.Size, SHA256: file.SHA256})
	}
	return out
}

func fenceRestore(ctx context.Context, fence func(context.Context, *os.Root, *os.Root) error, session *lockedMaintenance, operationID string, operation, stage, preimage *os.Root, syncDir func(*os.Root) error) error {
	if err := verifyDataBinding(ctx, session.dataDir, session.data); err != nil {
		return err
	}
	if err := verifyMaintenanceRootBinding(ctx, session.dataDir, session.data, session.maintenance); err != nil {
		return err
	}
	if err := verifyRestoreRoots(session, operationID, operation, stage, preimage); err != nil {
		return err
	}
	if err := syncRestoreDirectories(syncDir, stage, preimage, operation); err != nil {
		return err
	}
	if err := fence(ctx, session.data, session.maintenance); err != nil {
		return err
	}
	return ctx.Err()
}

func fenceRestoreMove(ctx context.Context, fence func(context.Context, *os.Root, *os.Root) error, syncDir func(*os.Root) error, session *lockedMaintenance, operationID string, operation, stage, preimage, destination, source *os.Root) error {
	if err := verifyDataBinding(ctx, session.dataDir, session.data); err != nil {
		return err
	}
	if err := verifyMaintenanceRootBinding(ctx, session.dataDir, session.data, session.maintenance); err != nil {
		return err
	}
	if err := verifyRestoreRoots(session, operationID, operation, stage, preimage); err != nil {
		return err
	}
	if err := syncRestoreMoveDirectories(syncDir, destination, source, operation); err != nil {
		return err
	}
	if err := fence(ctx, session.data, session.maintenance); err != nil {
		return err
	}
	return ctx.Err()
}

func closeRestoreFiles(files map[string]*os.File) { _ = closeFiles(files) }
