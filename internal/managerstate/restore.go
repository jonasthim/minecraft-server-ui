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
	"sort"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
)

const restoreFormat = 2
const restoreIntentLimit = 1 << 20

type RestoreOptions struct {
	SnapshotID             string
	Confirmed              bool
	ResumeBinaryVersion    string
	ResumeExecutableSHA256 string
}

type restoreHold struct {
	Format                       int    `json:"format"`
	Operation                    string `json:"operation"`
	Phase                        string `json:"phase"`
	SnapshotID                   string `json:"snapshot_id"`
	SnapshotManifestSHA256       string `json:"snapshot_manifest_sha256"`
	SafetySnapshotID             string `json:"safety_snapshot_id"`
	SafetySnapshotManifestSHA256 string `json:"safety_snapshot_manifest_sha256"`
	OperationID                  string `json:"operation_id"`
	IntentSHA256                 string `json:"intent_sha256"`
	BeforeSchema                 int64  `json:"before_schema"`
	TargetSchema                 int64  `json:"target_schema"`
	ExecutorVersion              string `json:"executor_version"`
	ExecutorSHA256               string `json:"executor_sha256"`
	ResumeVersion                string `json:"resume_version"`
	ResumeSHA256                 string `json:"resume_sha256"`
}

type restoreFile struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Identity string `json:"identity"`
}

type restoreIntent struct {
	Format                       int           `json:"format"`
	Operation                    string        `json:"operation"`
	ID                           string        `json:"id"`
	SnapshotID                   string        `json:"snapshot_id"`
	SnapshotManifestSHA256       string        `json:"snapshot_manifest_sha256"`
	SafetySnapshotID             string        `json:"safety_snapshot_id"`
	SafetySnapshotManifestSHA256 string        `json:"safety_snapshot_manifest_sha256"`
	BeforeSchema                 int64         `json:"before_schema"`
	TargetSchema                 int64         `json:"target_schema"`
	ExecutorVersion              string        `json:"executor_version"`
	ExecutorSHA256               string        `json:"executor_sha256"`
	ResumeVersion                string        `json:"resume_version"`
	ResumeSHA256                 string        `json:"resume_sha256"`
	PriorHoldSHA256              string        `json:"prior_hold_sha256,omitempty"`
	OldFiles                     []restoreFile `json:"old_files"`
	TargetFiles                  []restoreFile `json:"target_files"`
}

type restoreDeps struct {
	maintenanceDeps
	afterOldMove     func(int) error
	afterTargetMove  func(int) error
	afterHoldReplace func(*os.File)
	afterValidation  func() error
	syncDirectory    func(*os.Root) error
}

func Restore(ctx context.Context, dataDir, binaryVersion string, options RestoreOptions) (MaintenanceSummary, error) {
	return restoreWithDeps(ctx, dataDir, binaryVersion, options, restoreDeps{maintenanceDeps: productionMaintenanceDeps(), syncDirectory: syncDirectory})
}

func restoreWithDeps(ctx context.Context, dataDir, binaryVersion string, options RestoreOptions, deps restoreDeps) (summary MaintenanceSummary, retErr error) {
	if !options.Confirmed {
		return summary, maintenanceFailure("explicit maintenance confirmation is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return summary, maintenanceFailure("restore requires review or recovery", err)
	}
	if deps.imageHash == nil || deps.fence == nil || !validID(options.SnapshotID) || validateVersion(binaryVersion) != nil {
		return summary, maintenanceFailure("restore options are invalid", nil)
	}
	resumeVersion, resumeHash := options.ResumeBinaryVersion, options.ResumeExecutableSHA256
	if (resumeVersion == "") != (resumeHash == "") {
		return summary, maintenanceFailure("restore options are invalid", nil)
	}
	if resumeVersion != "" && (validateVersion(resumeVersion) != nil || !validDigest(resumeHash)) {
		return summary, maintenanceFailure("restore options are invalid", nil)
	}
	session, err := acquireLockedMaintenance(ctx, dataDir, deps.authorize, true)
	if err != nil {
		return summary, maintenanceFailure("restore could not acquire safe manager state", err)
	}
	defer func() {
		if closeErr := session.close(); closeErr != nil {
			retErr = errors.Join(retErr, maintenanceFailure("restore requires review or recovery", closeErr))
			summary = MaintenanceSummary{}
		}
	}()
	return restoreLocked(ctx, session, binaryVersion, options, resumeVersion, resumeHash, deps)
}

func restoreLocked(ctx context.Context, session *lockedMaintenance, binaryVersion string, options RestoreOptions, resumeVersion, resumeHash string, deps restoreDeps) (MaintenanceSummary, error) {
	if err := requireAbsent(session.maintenance, "hold.pending"); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore found pending maintenance state", err)
	}
	if _, err := session.maintenance.Lstat("hold.json"); err == nil {
		format, formatErr := currentMaintenanceHoldFormat(ctx, session.maintenance)
		if formatErr != nil {
			return MaintenanceSummary{}, maintenanceFailure("existing maintenance hold is invalid; manual recovery is required", formatErr)
		}
		if format == restoreFormat {
			return continueRestoreWithSession(ctx, session, binaryVersion, options, resumeVersion, resumeHash, deps)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return MaintenanceSummary{}, maintenanceFailure("restore could not inspect maintenance hold", err)
	}
	imageHash, err := deps.imageHash(ctx)
	if err != nil || !validDigest(imageHash) {
		return MaintenanceSummary{}, maintenanceFailure("running manager image could not be verified", err)
	}
	targetSummary, targetRecords, err := verifySnapshotRecords(ctx, session, options.SnapshotID)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("selected recovery snapshot failed verification", err)
	}
	if resumeVersion == "" {
		if targetSummary.Schema != db.LatestVersion() {
			return MaintenanceSummary{}, maintenanceFailure("restore of an older schema requires an explicit resume image binding", nil)
		}
		resumeVersion, resumeHash = binaryVersion, imageHash
	}
	if err := verifyDataBinding(ctx, session.dataDir, session.data); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("manager data binding changed before restore", err)
	}
	oldSources, err := sourceFiles(session.data)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("current manager database files are unsafe", err)
	}
	priorPin, priorHold, err := readOptionalFormatOneHold(ctx, session.maintenance)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("existing maintenance hold is invalid; manual recovery is required", err)
	}
	if priorPin != nil {
		defer priorPin.file.Close()
		if priorHold.Phase != "migrating" && priorHold.Phase != "migrated" {
			return MaintenanceSummary{}, maintenanceFailure("existing maintenance hold cannot be restored safely", nil)
		}
		priorSnapshot, _, verifyErr := verifySnapshotRecords(ctx, session, priorHold.SnapshotID)
		if verifyErr != nil || priorSnapshot.ManifestSHA256 != priorHold.SnapshotManifestSHA256 || priorSnapshot.Schema != priorHold.FromSchema {
			return MaintenanceSummary{}, maintenanceFailure("existing maintenance hold references an unverified snapshot", verifyErr)
		}
	}
	safety, err := snapshotLocked(ctx, session.dataDir, session.data, session.maintenance, session.managerSID, binaryVersion, nil, nil)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("validated safety snapshot could not be created", err)
	}
	safetySummary, safetyRecords, err := verifySnapshotRecords(ctx, session, safety.ID)
	if err != nil || safetySummary.ManifestSHA256 != safety.ManifestSHA256 || safetySummary.Schema != safety.Schema {
		return MaintenanceSummary{}, maintenanceFailure("validated safety snapshot could not be reverified", err)
	}
	if err := verifySourceSet(ctx, session.data, oldSources, safetyRecords); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("manager database changed during safety snapshot", err)
	}
	beforeVersion := safety.Schema
	operationID, err := newID()
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore operation could not be prepared", err)
	}
	operation, _, err := createPrivateDirectory(session.maintenance, operationID, session.managerSID)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore operation directory could not be created", err)
	}
	defer operation.Close()
	if err := operation.Mkdir("stage", 0700); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore staging directory could not be created", err)
	}
	if err := operation.Mkdir("preimage", 0700); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore preimage directory could not be created", err)
	}
	stage, err := openCheckedDirectory(operation, "stage", session.managerSID)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore staging directory could not be opened", err)
	}
	defer stage.Close()
	preimage, err := openCheckedDirectory(operation, "preimage", session.managerSID)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore preimage directory could not be opened", err)
	}
	defer preimage.Close()
	if err := verifyRestoreRoots(session, operationID, operation, stage, preimage); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore operation directory binding changed", err)
	}
	targetRoot, err := openCheckedDirectory(session.maintenance, options.SnapshotID, session.managerSID)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("selected recovery snapshot directory changed", err)
	}
	defer targetRoot.Close()
	targetOwned, err := copySnapshotForValidation(ctx, targetRoot, stage, targetRecords)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore target could not be staged", err)
	}
	defer closeOwnedFiles(targetOwned)
	targetFiles, err := recordsWithNativeIdentity(stage, targetOwned, targetRecords)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore target identity could not be recorded", err)
	}
	oldRecords, oldPins, err := captureRestoreFiles(ctx, session.data, oldSources, safetyRecords)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("current manager database identity could not be recorded", err)
	}
	defer closeFiles(oldPins)
	if priorPin != nil {
		if err := writeExclusiveFile(ctx, preimage, "prior-hold.json", priorPin.raw); err != nil {
			return MaintenanceSummary{}, maintenanceFailure("prior maintenance hold could not be preserved", err)
		}
	}
	intent := restoreIntent{Format: restoreFormat, Operation: "restore", ID: operationID, SnapshotID: targetSummary.ID, SnapshotManifestSHA256: targetSummary.ManifestSHA256, SafetySnapshotID: safety.ID, SafetySnapshotManifestSHA256: safety.ManifestSHA256, BeforeSchema: beforeVersion, TargetSchema: targetSummary.Schema, ExecutorVersion: binaryVersion, ExecutorSHA256: imageHash, ResumeVersion: resumeVersion, ResumeSHA256: resumeHash, OldFiles: oldRecords, TargetFiles: targetFiles}
	if priorPin != nil {
		sum := sha256.Sum256(priorPin.raw)
		intent.PriorHoldSHA256 = hex.EncodeToString(sum[:])
	}
	body, intentHash, err := marshalRestoreIntent(intent)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore intent could not be encoded", err)
	}
	if err := writeExclusiveFile(ctx, operation, "intent.json", body); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore intent could not be persisted", err)
	}
	checkedTarget, _, targetErr := verifySnapshotRecords(ctx, session, targetSummary.ID)
	checkedSafety, _, safetyErr := verifySnapshotRecords(ctx, session, safety.ID)
	if targetErr != nil || checkedTarget.ManifestSHA256 != targetSummary.ManifestSHA256 || safetyErr != nil || checkedSafety.ManifestSHA256 != safety.ManifestSHA256 {
		return MaintenanceSummary{}, maintenanceFailure("restore snapshots changed during preparation", errors.Join(targetErr, safetyErr))
	}
	if err := verifySourceSet(ctx, session.data, oldSources, safetyRecords); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("manager database changed during restore preparation", err)
	}
	for _, expected := range targetFiles {
		if err := verifyRestoreFileAt(ctx, stage, expected); err != nil {
			return MaintenanceSummary{}, maintenanceFailure("staged restore target changed during preparation", err)
		}
	}
	if err := fenceRestore(ctx, deps.fence, session, operationID, operation, stage, preimage, deps.syncDirectory); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore preparation durability is uncertain", err)
	}
	hold := restoreHold{Format: restoreFormat, Operation: "restore", Phase: "restoring", SnapshotID: targetSummary.ID, SnapshotManifestSHA256: targetSummary.ManifestSHA256, SafetySnapshotID: safety.ID, SafetySnapshotManifestSHA256: safety.ManifestSHA256, OperationID: operationID, IntentSHA256: intentHash, BeforeSchema: beforeVersion, TargetSchema: targetSummary.Schema, ExecutorVersion: binaryVersion, ExecutorSHA256: imageHash, ResumeVersion: resumeVersion, ResumeSHA256: resumeHash}
	pin, err := publishRestoreHold(ctx, session, priorPin, hold, deps.fence)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore hold publication is uncertain; review or recovery is required", err)
	}
	defer func() {
		if pin != nil && pin.file != nil {
			_ = pin.file.Close()
		}
	}()
	if err := applyRestoreMoves(ctx, session, operationID, operation, stage, preimage, intent, intentHash, pin, deps); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore stopped with recovery required", err)
	}
	if err := validateRestoredSet(ctx, session, operationID, operation, intent, deps.afterValidation); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restored manager database failed validation; review or recovery is required", err)
	}
	hold.Phase = "restored"
	newPin, err := replaceRestoreHold(ctx, session, pin, hold, deps.fence)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore completed with an uncertain hold; review or recovery is required", err)
	}
	if err := pin.file.Close(); err != nil {
		_ = newPin.file.Close()
		return MaintenanceSummary{}, maintenanceFailure("restore completed with an uncertain hold; review or recovery is required", err)
	}
	pin = newPin
	if deps.afterHoldReplace != nil {
		deps.afterHoldReplace(pin.file)
	}
	return MaintenanceSummary{Format: 1, Operation: "restore", State: "restored", SnapshotID: hold.SnapshotID, SafetySnapshotID: safety.ID, OperationID: operationID, BeforeSchema: hold.BeforeSchema, AfterSchema: hold.TargetSchema, ExecutableSHA256: hold.ResumeSHA256, ReviewRequired: true}, nil
}

func openRestoreHold(ctx context.Context, root *os.Root) (*maintenancePin, restoreHold, error) {
	info, err := root.Lstat("hold.json")
	if err != nil || !info.Mode().IsRegular() || checkOrdinaryFile(root, "hold.json", info) != nil {
		return nil, restoreHold{}, errors.New("restore hold is missing or unsafe")
	}
	file, err := root.Open("hold.json")
	if err != nil {
		return nil, restoreHold{}, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, restoreHold{}, errors.New("restore hold identity changed")
	}
	body, err := readMaintenanceFile(ctx, file, holdLimit)
	if err != nil {
		_ = file.Close()
		return nil, restoreHold{}, err
	}
	hold, err := parseRestoreHold(body)
	if err != nil {
		_ = file.Close()
		return nil, restoreHold{}, err
	}
	current, err := root.Lstat("hold.json")
	if err != nil || !sameFileState(info, current) || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, restoreHold{}, errors.New("restore hold changed while reading")
	}
	return &maintenancePin{file: file, info: info, raw: body}, hold, nil
}

func continueRestoreWithSession(ctx context.Context, session *lockedMaintenance, binaryVersion string, options RestoreOptions, resumeVersion, resumeHash string, deps restoreDeps) (MaintenanceSummary, error) {
	pin, hold, err := openRestoreHold(ctx, session.maintenance)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore requires a valid recovery hold", err)
	}
	defer func() {
		if pin != nil && pin.file != nil {
			_ = pin.file.Close()
		}
	}()
	imageHash, err := deps.imageHash(ctx)
	if err != nil || imageHash != hold.ExecutorSHA256 || binaryVersion != hold.ExecutorVersion || options.SnapshotID != hold.SnapshotID {
		return MaintenanceSummary{}, maintenanceFailure("restore continuation does not match the recovery hold", err)
	}
	if resumeVersion != "" && (resumeVersion != hold.ResumeVersion || resumeHash != hold.ResumeSHA256) {
		return MaintenanceSummary{}, maintenanceFailure("restore continuation does not match the recovery hold", nil)
	}
	if resumeVersion == "" && (binaryVersion != hold.ResumeVersion || imageHash != hold.ResumeSHA256) {
		return MaintenanceSummary{}, maintenanceFailure("restore continuation does not match the recovery hold", nil)
	}
	selected, _, err := verifySnapshotRecords(ctx, session, hold.SnapshotID)
	if err != nil || selected.ManifestSHA256 != hold.SnapshotManifestSHA256 || selected.Schema != hold.TargetSchema {
		return MaintenanceSummary{}, maintenanceFailure("selected recovery snapshot failed verification", err)
	}
	if err := verifyRawSnapshot(ctx, session, hold.SafetySnapshotID, hold.SafetySnapshotManifestSHA256); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("safety snapshot failed raw verification", err)
	}
	operation, err := openCheckedDirectory(session.maintenance, hold.OperationID, session.managerSID)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore operation directory changed", err)
	}
	defer operation.Close()
	stage, err := openCheckedDirectory(operation, "stage", session.managerSID)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore staging directory changed", err)
	}
	defer stage.Close()
	preimage, err := openCheckedDirectory(operation, "preimage", session.managerSID)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore preimage directory changed", err)
	}
	defer preimage.Close()
	intent, err := readRestoreIntent(ctx, operation, hold.IntentSHA256)
	if err != nil || !restoreIntentMatchesHold(intent, hold) {
		return MaintenanceSummary{}, maintenanceFailure("restore intent is invalid", err)
	}
	if err := verifyRestoreLocations(ctx, session, operation, hold.OperationID, stage, preimage, intent, false); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore file locations are ambiguous; manual recovery is required", err)
	}
	if err := verifyHoldPin(ctx, session.maintenance, "hold.json", pin); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore hold changed; manual recovery is required", err)
	}
	if hold.Phase == "restoring" {
		if err := applyRestoreMoves(ctx, session, hold.OperationID, operation, stage, preimage, intent, hold.IntentSHA256, pin, deps); err != nil {
			return MaintenanceSummary{}, maintenanceFailure("restore stopped with recovery required", err)
		}
		if err := validateRestoredSet(ctx, session, hold.OperationID, operation, intent, nil); err != nil {
			return MaintenanceSummary{}, maintenanceFailure("restored manager database failed validation; review or recovery is required", err)
		}
		hold.Phase = "restored"
		updated, err := replaceRestoreHold(ctx, session, pin, hold, deps.fence)
		if err != nil {
			return MaintenanceSummary{}, maintenanceFailure("restore completed with an uncertain hold; review or recovery is required", err)
		}
		if err := pin.file.Close(); err != nil {
			_ = updated.file.Close()
			return MaintenanceSummary{}, maintenanceFailure("restore completed with an uncertain hold; review or recovery is required", err)
		}
		pin = updated
		if deps.afterHoldReplace != nil {
			deps.afterHoldReplace(pin.file)
		}
	} else {
		if err := verifyCompletedRestore(ctx, session, operation, stage, preimage, intent); err != nil {
			return MaintenanceSummary{}, maintenanceFailure("restored manager database failed validation; review or recovery is required", err)
		}
	}
	return MaintenanceSummary{Format: 1, Operation: "restore", State: "restored", SnapshotID: hold.SnapshotID, SafetySnapshotID: hold.SafetySnapshotID, OperationID: hold.OperationID, BeforeSchema: hold.BeforeSchema, AfterSchema: hold.TargetSchema, ExecutableSHA256: hold.ResumeSHA256, ReviewRequired: true}, nil
}

func restoreIntentMatchesHold(intent restoreIntent, hold restoreHold) bool {
	return intent.Format == restoreFormat && intent.Operation == "restore" && intent.ID == hold.OperationID && intent.SnapshotID == hold.SnapshotID && intent.SnapshotManifestSHA256 == hold.SnapshotManifestSHA256 && intent.SafetySnapshotID == hold.SafetySnapshotID && intent.SafetySnapshotManifestSHA256 == hold.SafetySnapshotManifestSHA256 && intent.BeforeSchema == hold.BeforeSchema && intent.TargetSchema == hold.TargetSchema && intent.ExecutorVersion == hold.ExecutorVersion && intent.ExecutorSHA256 == hold.ExecutorSHA256 && intent.ResumeVersion == hold.ResumeVersion && intent.ResumeSHA256 == hold.ResumeSHA256
}

func verifyCompletedRestore(ctx context.Context, session *lockedMaintenance, operation, stage, preimage *os.Root, intent restoreIntent) error {
	if err := verifyRestoreLocations(ctx, session, operation, intent.ID, stage, preimage, intent, false); err != nil {
		return err
	}
	for _, expected := range intent.TargetFiles {
		if err := verifyRestoreFileAt(ctx, session.data, expected); err != nil {
			return err
		}
	}
	if err := verifyLiveSQLiteEntrySet(ctx, session.data, intent); err != nil {
		return err
	}
	return validateRestoredSet(ctx, session, intent.ID, nil, intent, nil)
}

func resumeRestoreWithSession(ctx context.Context, session *lockedMaintenance, binaryVersion, snapshotID string, deps maintenanceDeps) (MaintenanceSummary, error) {
	pin, hold, err := openRestoreHold(ctx, session.maintenance)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("resume requires a valid restored hold", err)
	}
	defer func() {
		if pin != nil && pin.file != nil {
			_ = pin.file.Close()
		}
	}()
	if hold.Phase != "restored" || hold.SnapshotID != snapshotID || hold.ResumeVersion != binaryVersion || hold.TargetSchema != db.LatestVersion() {
		return MaintenanceSummary{}, maintenanceFailure("resume does not match the restored maintenance hold", nil)
	}
	imageHash, err := deps.imageHash(ctx)
	if err != nil || imageHash != hold.ResumeSHA256 {
		return MaintenanceSummary{}, maintenanceFailure("current manager image does not match the restored hold", err)
	}
	selected, _, err := verifySnapshotRecords(ctx, session, snapshotID)
	if err != nil || selected.ManifestSHA256 != hold.SnapshotManifestSHA256 || selected.Schema != hold.TargetSchema {
		return MaintenanceSummary{}, maintenanceFailure("selected recovery snapshot failed verification", err)
	}
	if err := verifyRawSnapshot(ctx, session, hold.SafetySnapshotID, hold.SafetySnapshotManifestSHA256); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("safety snapshot failed raw verification", err)
	}
	operation, err := openCheckedDirectory(session.maintenance, hold.OperationID, session.managerSID)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore operation directory changed", err)
	}
	defer operation.Close()
	stage, err := openCheckedDirectory(operation, "stage", session.managerSID)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore staging directory changed", err)
	}
	defer stage.Close()
	preimage, err := openCheckedDirectory(operation, "preimage", session.managerSID)
	if err != nil {
		return MaintenanceSummary{}, maintenanceFailure("restore preimage directory changed", err)
	}
	defer preimage.Close()
	intent, err := readRestoreIntent(ctx, operation, hold.IntentSHA256)
	if err != nil || !restoreIntentMatchesHold(intent, hold) {
		return MaintenanceSummary{}, maintenanceFailure("restore intent is invalid", err)
	}
	if err := verifyCompletedRestore(ctx, session, operation, stage, preimage, intent); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("current manager database requires review or recovery", err)
	}
	if err := verifyHoldPin(ctx, session.maintenance, "hold.json", pin); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("maintenance hold changed before resume", err)
	}
	if err := session.maintenance.Remove("hold.json"); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("maintenance hold removal is uncertain", err)
	}
	if err := pin.file.Close(); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("maintenance hold removal is uncertain", err)
	}
	pin.file = nil
	if err := deps.fence(ctx, session.data, session.maintenance); err != nil {
		return MaintenanceSummary{}, maintenanceFailure("maintenance hold removal is uncertain; review or recovery is required", err)
	}
	return MaintenanceSummary{Format: 1, Operation: "restore", State: "resumed", SnapshotID: hold.SnapshotID, SafetySnapshotID: hold.SafetySnapshotID, OperationID: hold.OperationID, BeforeSchema: hold.BeforeSchema, AfterSchema: hold.TargetSchema, ExecutableSHA256: hold.ResumeSHA256, ReviewRequired: false}, nil
}

func verifyRawSnapshot(ctx context.Context, session *lockedMaintenance, id, expectedManifestHash string) error {
	if !validID(id) || !validDigest(expectedManifestHash) {
		return errors.New("safety snapshot binding is invalid")
	}
	root, err := openCheckedDirectory(session.maintenance, id, session.managerSID)
	if err != nil {
		return err
	}
	defer root.Close()
	meta, body, err := readManifest(root)
	if err != nil || meta.Format != formatVersion || meta.ID != id || meta.Schema < 1 || validateVersion(meta.BinaryVersion) != nil || len(meta.Files) == 0 || meta.Files[0].Name != "manager.db" {
		return errors.New("safety manifest is invalid")
	}
	if restoreHashForBytes(body) != expectedManifestHash {
		return errors.New("safety manifest hash changed")
	}
	entries, err := snapshotEntries(root)
	if err != nil || len(entries) != len(meta.Files)+1 {
		return errors.New("safety snapshot file set changed")
	}
	for index, record := range meta.Files {
		if index >= len(entries) || entries[index].name != record.Name || !allowedFiles[record.Name] || record.Size < 0 || !validDigest(record.SHA256) {
			return errors.New("safety snapshot file set changed")
		}
		if _, err := verifyFile(ctx, root, record); err != nil {
			return err
		}
	}
	if entries[len(entries)-1].name != manifestName {
		return errors.New("safety snapshot manifest missing")
	}
	return nil
}

func validateRestoreHold(hold restoreHold) bool {
	return hold.Format == restoreFormat && hold.Operation == "restore" && (hold.Phase == "restoring" || hold.Phase == "restored") && validID(hold.SnapshotID) && validDigest(hold.SnapshotManifestSHA256) && validID(hold.SafetySnapshotID) && validDigest(hold.SafetySnapshotManifestSHA256) && validID(hold.OperationID) && validDigest(hold.IntentSHA256) && hold.BeforeSchema >= 1 && hold.TargetSchema >= 1 && validateVersion(hold.ExecutorVersion) == nil && validDigest(hold.ExecutorSHA256) && validateVersion(hold.ResumeVersion) == nil && validDigest(hold.ResumeSHA256)
}

func parseRestoreHold(body []byte) (restoreHold, error) {
	var hold restoreHold
	if len(body) == 0 || len(body) > holdLimit {
		return hold, errors.New("restore hold exceeds bound")
	}
	if err := strictHoldJSON(body, &hold); err != nil || !validateRestoreHold(hold) {
		return hold, errors.New("restore hold is invalid")
	}
	return hold, nil
}

func marshalRestoreIntent(intent restoreIntent) ([]byte, string, error) {
	if !validID(intent.ID) || intent.Format != restoreFormat || intent.Operation != "restore" || !validID(intent.SnapshotID) || !validDigest(intent.SnapshotManifestSHA256) || !validID(intent.SafetySnapshotID) || !validDigest(intent.SafetySnapshotManifestSHA256) || (intent.PriorHoldSHA256 != "" && !validDigest(intent.PriorHoldSHA256)) || intent.BeforeSchema < 1 || intent.TargetSchema < 1 || validateVersion(intent.ExecutorVersion) != nil || !validDigest(intent.ExecutorSHA256) || validateVersion(intent.ResumeVersion) != nil || !validDigest(intent.ResumeSHA256) || !validFileRecords(intent.OldFiles) || !validFileRecords(intent.TargetFiles) {
		return nil, "", errors.New("restore intent fields are invalid")
	}
	seenIdentities := make(map[string]bool, len(intent.OldFiles)+len(intent.TargetFiles))
	for _, file := range append(append([]restoreFile(nil), intent.OldFiles...), intent.TargetFiles...) {
		if seenIdentities[file.Identity] {
			return nil, "", errors.New("restore intent reuses a native file identity")
		}
		seenIdentities[file.Identity] = true
	}
	body, err := json.Marshal(intent)
	if err != nil || len(body) == 0 || len(body) > restoreIntentLimit {
		return nil, "", errors.New("restore intent exceeds bound")
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}

func readRestoreIntent(ctx context.Context, root *os.Root, expected string) (restoreIntent, error) {
	file, err := root.Open("intent.json")
	if err != nil {
		return restoreIntent{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > restoreIntentLimit || checkOpenFile(file, info) != nil {
		return restoreIntent{}, errors.New("restore intent is unsafe")
	}
	body, err := readMaintenanceFile(ctx, file, restoreIntentLimit)
	if err != nil || len(body) > restoreIntentLimit {
		return restoreIntent{}, errors.New("restore intent exceeds bound")
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != expected {
		return restoreIntent{}, errors.New("restore intent hash changed")
	}
	after, statErr := file.Stat()
	pathInfo, pathErr := root.Lstat("intent.json")
	if statErr != nil || pathErr != nil || !sameFileState(info, after) || !os.SameFile(info, pathInfo) || checkOrdinaryFile(root, "intent.json", pathInfo) != nil {
		return restoreIntent{}, errors.New("restore intent path changed")
	}
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return restoreIntent{}, err
	}
	var intent restoreIntent
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		return restoreIntent{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return restoreIntent{}, errors.New("restore intent has trailing data")
	}
	encoded, digest, err := marshalRestoreIntent(intent)
	if err != nil || !bytes.Equal(encoded, body) || digest != expected {
		return restoreIntent{}, errors.New("restore intent is not canonical")
	}
	return intent, nil
}

func validFileRecords(files []restoreFile) bool {
	if len(files) == 0 || len(files) > len(allowedFiles) || files[0].Name != "manager.db" {
		return false
	}
	last := ""
	identities := make(map[string]bool, len(files))
	for _, file := range files {
		if !allowedFiles[file.Name] || file.Size < 0 || (file.Name == "manager.db" && file.Size == 0) || !validDigest(file.SHA256) || !validRestoreNativeIdentity(file.Identity) || identities[file.Identity] || (last != "" && file.Name <= last) {
			return false
		}
		identities[file.Identity] = true
		last = file.Name
	}
	return true
}

func sortedRestoreFiles(files []restoreFile) []restoreFile {
	out := append([]restoreFile(nil), files...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func restoreHoldDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func restoreHashForBytes(body []byte) string { return restoreHoldDigest(body) }

func restoreNameValid(name string) bool { return allowedFiles[name] }
