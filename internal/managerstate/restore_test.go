package managerstate

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

func TestRestoreRequiresExplicitConfirmationBeforeOpeningState(t *testing.T) {
	_, err := Restore(context.Background(), "relative/path", "restore-version", RestoreOptions{SnapshotID: "invalid"})
	if err == nil || err.Error() != "explicit maintenance confirmation is required" {
		t.Fatalf("Restore without confirmation error = %v", err)
	}
}

func TestRestoreRejectsPartialExpectedResumeBinding(t *testing.T) {
	_, err := Restore(context.Background(), "relative/path", "restore-version", RestoreOptions{
		SnapshotID: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Confirmed:  true, ResumeBinaryVersion: "future-version",
	})
	if err == nil || err.Error() != "restore options are invalid" {
		t.Fatalf("Restore with partial resume binding error = %v", err)
	}
}

func TestRestorePreservesCancellationCause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Restore(ctx, "/tmp/unused", "restore-version", RestoreOptions{Confirmed: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Restore cancellation cause = %v", err)
	}
}

func TestRestoreContextReaderStopsHashingAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	reader := &cancelOnFirstRead{cancel: cancel}
	var digest bytes.Buffer
	_, err := io.Copy(&digest, &restoreContextReader{ctx: ctx, reader: reader})
	if !errors.Is(err, context.Canceled) || reader.reads != 1 {
		t.Fatalf("context reader result = %v after %d reads", err, reader.reads)
	}
}

type cancelOnFirstRead struct {
	cancel context.CancelFunc
	reads  int
}

func (r *cancelOnFirstRead) Read(p []byte) (int, error) {
	r.reads++
	if r.reads == 1 {
		for index := range p {
			p[index] = 'x'
		}
		r.cancel()
		return len(p), nil
	}
	return 0, io.EOF
}

func TestRestorePublishesSafetyHoldAndRequiresExplicitResume(t *testing.T) {
	dir, selected := latestRestoreFixture(t)
	worldPath := filepath.Join(dir, "worlds", "old-world", "level.dat")
	if err := os.MkdirAll(filepath.Dir(worldPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worldPath, []byte("world marker stays unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	setInstanceName(t, dir, "Changed after snapshot")
	result, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, restoreDeps{maintenanceDeps: testMaintenanceDeps()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Operation != "restore" || result.State != "restored" || result.SnapshotID != selected.ID || result.SafetySnapshotID == "" || result.OperationID == "" || result.BeforeSchema != db.LatestVersion() || result.AfterSchema != selected.Schema || !result.ReviewRequired {
		t.Fatalf("unexpected restore summary: %+v", result)
	}
	if got := instanceName(t, dir); got != "Preserved server" {
		t.Fatalf("restored name = %q", got)
	}
	if err := CheckStartup(t.Context(), dir); err == nil {
		t.Fatal("startup was not blocked after restore")
	}
	worldAfter, err := os.ReadFile(worldPath)
	if err != nil || string(worldAfter) != "world marker stays unchanged" {
		t.Fatalf("world marker changed during manager-state restore: %v", err)
	}
	safety, err := Verify(t.Context(), dir, result.SafetySnapshotID)
	if err != nil || safety.Schema != db.LatestVersion() {
		t.Fatalf("safety snapshot verification = %+v, %v", safety, err)
	}
	if got := snapshotInstanceName(t, dir, result.SafetySnapshotID); got != "Changed after snapshot" {
		t.Fatalf("safety snapshot did not preserve pre-restore state: %q", got)
	}
	resumed, err := resumeWithDeps(t.Context(), dir, "current-version", selected.ID, true, testMaintenanceDeps())
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Operation != "restore" || resumed.State != "resumed" || resumed.ReviewRequired || resumed.OperationID != result.OperationID {
		t.Fatalf("unexpected resume summary: %+v", resumed)
	}
	if err := CheckStartup(t.Context(), dir); err != nil {
		t.Fatalf("startup remained blocked after resume: %v", err)
	}
}

func TestRestoreSyncsRenameDestinationBeforeSourceOnProductionMovePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses the fixed-volume durability fence")
	}
	dir, selected := latestRestoreFixture(t)
	setInstanceName(t, dir, "Changed after snapshot")
	var synced []string
	deps := restoreDeps{maintenanceDeps: testMaintenanceDeps(), syncDirectory: func(root *os.Root) error {
		synced = append(synced, root.Name())
		return nil
	}}
	assertMoveOrder := func(destination, sourceSuffix string) error {
		if len(synced) < 3 {
			return errors.New("move did not sync destination and source directories")
		}
		last := synced[len(synced)-3:]
		if last[0] != destination && !strings.HasSuffix(last[0], destination) || !strings.HasSuffix(last[1], sourceSuffix) || !strings.Contains(last[2], string(filepath.Separator)+"maintenance"+string(filepath.Separator)) {
			return fmt.Errorf("unexpected rename sync order: %q", last)
		}
		return nil
	}
	deps.afterOldMove = func(int) error {
		if err := assertMoveOrder(filepath.Join("preimage"), dir); err != nil {
			return err
		}
		synced = nil
		return nil
	}
	deps.afterTargetMove = func(int) error {
		if err := assertMoveOrder(dir, "stage"); err != nil {
			return err
		}
		synced = nil
		return nil
	}
	_, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, deps)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRestoreResumeRefusesWrongBindingAndChangedLiveDatabase(t *testing.T) {
	for _, name := range []string{"wrong snapshot", "wrong image", "changed live file"} {
		t.Run(name, func(t *testing.T) {
			dir, selected := latestRestoreFixture(t)
			result, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, restoreDeps{maintenanceDeps: testMaintenanceDeps()})
			if err != nil {
				t.Fatal(err)
			}
			id, version := selected.ID, "current-version"
			deps := testMaintenanceDeps()
			switch name {
			case "wrong snapshot":
				id = selected.ID + "0"
			case "wrong image":
				deps.imageHash = func(context.Context) (string, error) {
					return "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", nil
				}
			case "changed live file":
				setInstanceName(t, dir, "unreviewed state")
			}
			if _, err := resumeWithDeps(t.Context(), dir, version, id, true, deps); err == nil {
				t.Fatal("Resume accepted a mismatched or changed restore state")
			}
			if err := CheckStartup(t.Context(), dir); err == nil {
				t.Fatal("failed Resume removed the startup hold")
			}
			if name != "changed live file" && result.OperationID == "" {
				t.Fatal("restore summary omitted operation identity")
			}
		})
	}
}

func TestRestoreRechecksLiveFilesAfterValidationAndPreservesReplacement(t *testing.T) {
	dir, selected := latestRestoreFixture(t)
	foreign := []byte("foreign replacement after sqlite validation")
	deps := restoreDeps{maintenanceDeps: testMaintenanceDeps(), afterValidation: func() error {
		return os.WriteFile(filepath.Join(dir, "manager.db"), foreign, 0600)
	}}
	if _, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, deps); err == nil {
		t.Fatal("restore accepted a live database replacement after validation")
	}
	got, err := os.ReadFile(filepath.Join(dir, "manager.db"))
	if err != nil || !bytes.Equal(got, foreign) {
		t.Fatalf("foreign replacement was not preserved: %q, %v", got, err)
	}
	if err := CheckStartup(t.Context(), dir); err == nil {
		t.Fatal("post-validation substitution cleared the recovery hold")
	}
}

func TestRestoreInvalidCurrentDatabaseRefusesBeforeCreatingHold(t *testing.T) {
	dir, selected := latestRestoreFixture(t)
	path := filepath.Join(dir, "manager.db")
	if err := os.WriteFile(path, []byte("invalid current database bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, restoreDeps{maintenanceDeps: testMaintenanceDeps()}); err == nil {
		t.Fatal("restore accepted an invalid current database")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("invalid live database was changed: %v", err)
	}
	if err := CheckStartup(t.Context(), dir); err != nil {
		t.Fatalf("preparation failure unexpectedly left a hold: %v", err)
	}
}

func TestRestoreRefusesHardlinkedSidecarBeforeCreatingHold(t *testing.T) {
	dir, selected := latestRestoreFixture(t)
	if err := os.Link(filepath.Join(dir, "manager.db"), filepath.Join(dir, "manager.db-wal")); err != nil {
		t.Skip("filesystem does not permit a hardlink fixture")
	}
	if _, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, restoreDeps{maintenanceDeps: testMaintenanceDeps()}); err == nil {
		t.Fatal("restore accepted a hardlinked SQLite sidecar")
	}
	if err := CheckStartup(t.Context(), dir); err != nil {
		t.Fatalf("unsafe sidecar refusal unexpectedly left a hold: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "manager.db-wal")); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreContinuationRejectsSameBytesAtReplacementIdentity(t *testing.T) {
	dir, selected := latestRestoreFixture(t)
	setInstanceName(t, dir, "Changed after snapshot")
	_, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, restoreDeps{maintenanceDeps: testMaintenanceDeps(), afterOldMove: func(int) error { return errors.New("injected boundary") }})
	if err == nil {
		t.Fatal("restore boundary was not injected")
	}
	hold, err := readCurrentRestoreHoldForTest(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	stagePath := filepath.Join(dir, "maintenance", hold.OperationID, "stage", "manager.db")
	bytesBefore, err := os.ReadFile(stagePath)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(stagePath)
	if err != nil {
		t.Fatal(err)
	}
	originalIdentity, err := restoreNativeIdentity(original)
	closeErr := original.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("record original stage identity: identity_error=%v close_error=%v", err, closeErr)
	}
	replacementPath := stagePath + ".replacement"
	replacement, err := os.OpenFile(replacementPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(replacementPath) })
	if _, err := replacement.Write(bytesBefore); err != nil {
		_ = replacement.Close()
		t.Fatal(err)
	}
	if err := replacement.Sync(); err != nil {
		_ = replacement.Close()
		t.Fatal(err)
	}
	replacementIdentity, err := restoreNativeIdentity(replacement)
	closeErr = replacement.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("record replacement stage identity: identity_error=%v close_error=%v", err, closeErr)
	}
	if replacementIdentity == originalIdentity {
		t.Fatal("replacement was not independently created while the original stage inode remained allocated")
	}
	if err := os.Rename(replacementPath, stagePath); err != nil {
		t.Fatalf("atomically replace staged file: %v", err)
	}
	installed, err := os.Open(stagePath)
	if err != nil {
		t.Fatal(err)
	}
	installedIdentity, identityErr := restoreNativeIdentity(installed)
	closeErr = installed.Close()
	if identityErr != nil || closeErr != nil || installedIdentity != replacementIdentity {
		t.Fatalf("atomic replacement identity changed: got=%q want=%q identity_error=%v close_error=%v", installedIdentity, replacementIdentity, identityErr, closeErr)
	}
	if _, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, restoreDeps{maintenanceDeps: testMaintenanceDeps()}); err == nil {
		t.Fatal("continuation accepted same bytes at an unrecorded native identity")
	}
	bytesAfter, err := os.ReadFile(stagePath)
	if err != nil || !bytes.Equal(bytesBefore, bytesAfter) {
		t.Fatalf("replacement stage file was not preserved: %v", err)
	}
	if err := CheckStartup(t.Context(), dir); err == nil {
		t.Fatal("identity refusal cleared restore hold")
	}
}

func TestRestoreResumeRefusesExtraSQLiteSidecarAndPreservesIt(t *testing.T) {
	dir, selected := latestRestoreFixture(t)
	result, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, restoreDeps{maintenanceDeps: testMaintenanceDeps()})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manager.db-journal")
	foreign := []byte("unowned sidecar marker")
	if err := os.WriteFile(path, foreign, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resumeWithDeps(t.Context(), dir, "current-version", selected.ID, true, testMaintenanceDeps()); err == nil {
		t.Fatal("Resume accepted an unmanifested SQLite sidecar")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, foreign) {
		t.Fatalf("unowned sidecar was changed: %v", err)
	}
	if err := CheckStartup(t.Context(), dir); err == nil {
		t.Fatal("sidecar refusal cleared restore hold")
	}
	if result.OperationID == "" {
		t.Fatal("restore summary omitted operation ID")
	}
}

func TestRestoreContinuesOnlyFromPinnedIntentAfterEachMoveClass(t *testing.T) {
	for _, point := range []string{"after-old-move", "after-target-move"} {
		t.Run(point, func(t *testing.T) {
			dir, selected := latestRestoreFixture(t)
			setInstanceName(t, dir, "Changed after snapshot")
			deps := restoreDeps{maintenanceDeps: testMaintenanceDeps()}
			injected := errors.New("injected interruption")
			if point == "after-old-move" {
				deps.afterOldMove = func(int) error {
					release, lockErr := launcher.AcquireManager(dir)
					if lockErr == nil {
						release()
						return errors.New("restore did not retain manager lock")
					}
					return injected
				}
			} else {
				deps.afterTargetMove = func(int) error { return injected }
			}
			_, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, deps)
			if err == nil || err.Error() == "" {
				t.Fatal("restore did not report the injected interruption")
			}
			if err := CheckStartup(t.Context(), dir); err == nil {
				t.Fatal("interrupted restore did not retain startup hold")
			}
			result, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, restoreDeps{maintenanceDeps: testMaintenanceDeps()})
			if err != nil {
				t.Fatalf("explicit continuation failed: %v", err)
			}
			if result.State != "restored" || result.Operation != "restore" || instanceName(t, dir) != "Preserved server" {
				t.Fatalf("continuation did not finish target state: %+v", result)
			}
		})
	}
}

func TestRestoreCancellationAfterMoveKeepsHoldAndCanBeExplicitlyContinued(t *testing.T) {
	dir, selected := latestRestoreFixture(t)
	setInstanceName(t, dir, "Changed after snapshot")
	ctx, cancel := context.WithCancel(t.Context())
	deps := restoreDeps{maintenanceDeps: testMaintenanceDeps(), afterOldMove: func(int) error { cancel(); return nil }}
	_, err := restoreWithDeps(ctx, dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, deps)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("restore cancellation cause = %v", err)
	}
	if err := CheckStartup(t.Context(), dir); err == nil {
		t.Fatal("canceled restore cleared its hold")
	}
	result, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, restoreDeps{maintenanceDeps: testMaintenanceDeps()})
	if err != nil || result.State != "restored" {
		t.Fatalf("explicit continuation after cancellation = %+v, %v", result, err)
	}
}

func TestRestorePreservesExistingMigrationHoldAsExactPreimage(t *testing.T) {
	dir, snapshot := olderMaintenanceFixture(t)
	if _, err := migrateWithDeps(t.Context(), dir, "current-version", MigrationOptions{SnapshotID: snapshot.ID, FromSchema: 1, ToSchema: db.LatestVersion()}, testMaintenanceDeps()); err != nil {
		t.Fatal(err)
	}
	priorPath := filepath.Join(dir, "maintenance", "hold.json")
	prior, err := os.ReadFile(priorPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: snapshot.ID, Confirmed: true, ResumeBinaryVersion: "older-version", ResumeExecutableSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, restoreDeps{maintenanceDeps: testMaintenanceDeps()})
	if err != nil {
		t.Fatal(err)
	}
	hold, err := readCurrentRestoreHoldForTest(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := os.ReadFile(filepath.Join(dir, "maintenance", hold.OperationID, "intent.json"))
	if err != nil {
		t.Fatal(err)
	}
	var intent restoreIntent
	if err := json.Unmarshal(operation, &intent); err != nil {
		t.Fatal(err)
	}
	if intent.PriorHoldSHA256 == "" || restoreHashForBytes(prior) != intent.PriorHoldSHA256 {
		t.Fatal("prior hold bytes were not bound into immutable intent")
	}
	preimage, err := os.ReadFile(filepath.Join(dir, "maintenance", hold.OperationID, "preimage", "prior-hold.json"))
	if err != nil || !bytes.Equal(preimage, prior) {
		t.Fatalf("prior hold preimage changed: err=%v", err)
	}
	if result.OperationID != hold.OperationID || result.AfterSchema != 1 || instanceName(t, dir) != "Preserved server" {
		t.Fatalf("unexpected older-schema restore result: %+v", result)
	}
	if err := CheckStartup(t.Context(), dir); err == nil {
		t.Fatal("restore hold did not remain after older-schema target")
	}
}

func TestRestoreFormatTwoParserIsStrictButSchemaIndependent(t *testing.T) {
	hold := restoreHold{Format: 2, Operation: "restore", Phase: "restoring", SnapshotID: strings.Repeat("a", 64), SnapshotManifestSHA256: strings.Repeat("b", 64), SafetySnapshotID: strings.Repeat("c", 64), SafetySnapshotManifestSHA256: strings.Repeat("d", 64), OperationID: strings.Repeat("e", 64), IntentSHA256: strings.Repeat("f", 64), BeforeSchema: db.LatestVersion() + 2, TargetSchema: db.LatestVersion() + 1, ExecutorVersion: "restore-version", ExecutorSHA256: strings.Repeat("1", 64), ResumeVersion: "resume-version", ResumeSHA256: strings.Repeat("2", 64)}
	body, err := json.Marshal(hold)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseRestoreHold(body); err != nil {
		t.Fatalf("primitive hold parser tied itself to current schema: %v", err)
	}
	if _, err := parseRestoreHold(append(bytes.TrimSuffix(body, []byte("}")), []byte(`,"unknown":true}`)...)); err == nil {
		t.Fatal("format-2 parser accepted an unknown field")
	}
	if _, err := parseRestoreHold([]byte(`{"format":2,"format":2}`)); err == nil {
		t.Fatal("format-2 parser accepted duplicate keys")
	}
}

func TestRestoreClosesReplacedHoldPinBeforeReturning(t *testing.T) {
	dir, selected := latestRestoreFixture(t)
	var retained *os.File
	deps := restoreDeps{maintenanceDeps: testMaintenanceDeps(), afterHoldReplace: func(file *os.File) { retained = file }}
	if _, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, deps); err != nil {
		t.Fatal(err)
	}
	if retained == nil {
		t.Fatal("restore did not replace the durable hold")
	}
	if _, err := retained.Stat(); err == nil {
		t.Fatal("restore leaked the final hold handle")
	}
}

func TestRestoreMovesCommittedWALFileSetAsAUnit(t *testing.T) {
	dir, selected := committedWALRestoreFixture(t)
	result, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, restoreDeps{maintenanceDeps: testMaintenanceDeps()})
	if err != nil {
		t.Fatal(err)
	}
	intentBody, err := os.ReadFile(filepath.Join(dir, "maintenance", result.OperationID, "intent.json"))
	if err != nil {
		t.Fatal(err)
	}
	var intent restoreIntent
	if err := json.Unmarshal(intentBody, &intent); err != nil {
		t.Fatal(err)
	}
	foundWAL := false
	for _, file := range intent.TargetFiles {
		if file.Name == "manager.db-wal" {
			foundWAL = true
		}
	}
	if !foundWAL || len(intent.TargetFiles) != selected.FileCount {
		t.Fatalf("restore intent omitted complete captured WAL file set: %+v", intent.TargetFiles)
	}
	if _, err := resumeWithDeps(t.Context(), dir, "current-version", selected.ID, true, testMaintenanceDeps()); err != nil {
		t.Fatal(err)
	}
	if got := instanceName(t, dir); got != "Committed in WAL" {
		t.Fatalf("restored WAL transaction missing: %q", got)
	}
}

func TestRestoreContinuesAfterEveryTargetFileMove(t *testing.T) {
	_, probe := committedWALRestoreFixture(t)
	for failedMove := 0; failedMove < probe.FileCount; failedMove++ {
		t.Run(fmt.Sprintf("move-%d", failedMove), func(t *testing.T) {
			dir, selected := committedWALRestoreFixture(t)
			deps := restoreDeps{maintenanceDeps: testMaintenanceDeps(), afterTargetMove: func(index int) error {
				if index == failedMove {
					return errors.New("injected target boundary")
				}
				return nil
			}}
			_, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, deps)
			if err == nil {
				t.Fatal("injection did not interrupt target move")
			}
			if err := CheckStartup(t.Context(), dir); err == nil {
				t.Fatal("interrupted target move did not retain startup hold")
			}
			result, err := restoreWithDeps(t.Context(), dir, "current-version", RestoreOptions{SnapshotID: selected.ID, Confirmed: true}, restoreDeps{maintenanceDeps: testMaintenanceDeps()})
			if err != nil {
				t.Fatalf("explicit target continuation failed: %v", err)
			}
			if result.State != "restored" {
				t.Fatalf("continuation state = %q", result.State)
			}
		})
	}
}

func committedWALRestoreFixture(t *testing.T) (string, Summary) {
	t.Helper()
	dir, _ := olderMaintenanceFixture(t)
	database, err := sql.Open("sqlite", db.FileURI(filepath.Join(dir, "manager.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context(), database); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE instances SET name='Committed in WAL' WHERE id='old-world'"); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	walInfo, err := os.Stat(filepath.Join(dir, "manager.db-wal"))
	if err != nil || walInfo.Size() == 0 {
		_ = database.Close()
		t.Skip("SQLite runtime did not retain a committed WAL for this fixture")
	}
	selected, err := Snapshot(t.Context(), dir, "capture-version")
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if selected.FileCount < 2 {
		t.Fatal("raw snapshot omitted the committed WAL sidecar")
	}
	setInstanceName(t, dir, "Live state after WAL snapshot")
	return dir, selected
}

func TestRestoreIntentRejectsMalformedNativeFileIdentity(t *testing.T) {
	if validFileRecords([]restoreFile{{Name: "manager.db", Size: 1, SHA256: strings.Repeat("a", 64), Identity: "not-a-native-identity"}}) {
		t.Fatal("intent accepted an identity that cannot be emitted by the current platform")
	}
}

func readCurrentRestoreHoldForTest(t *testing.T, dir string) (restoreHold, error) {
	t.Helper()
	root, err := os.OpenRoot(filepath.Join(dir, "maintenance"))
	if err != nil {
		return restoreHold{}, err
	}
	defer root.Close()
	pin, hold, err := openRestoreHold(t.Context(), root)
	if pin != nil {
		_ = pin.file.Close()
	}
	return hold, err
}

func latestRestoreFixture(t *testing.T) (string, Summary) {
	t.Helper()
	dir, _ := olderMaintenanceFixture(t)
	database, err := sql.Open("sqlite", db.FileURI(filepath.Join(dir, "manager.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context(), database); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "manager.db"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Snapshot(t.Context(), dir, "capture-version")
	if err != nil {
		t.Fatal(err)
	}
	return dir, snapshot
}

func setInstanceName(t *testing.T, dir, name string) {
	t.Helper()
	database, err := sql.Open("sqlite", db.FileURI(filepath.Join(dir, "manager.db")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE instances SET name=? WHERE id='old-world'", name); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func instanceName(t *testing.T, dir string) string {
	t.Helper()
	database, err := sql.Open("sqlite", db.FileURI(filepath.Join(dir, "manager.db"))+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var name string
	if err := database.QueryRow("SELECT name FROM instances WHERE id='old-world'").Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}

func snapshotInstanceName(t *testing.T, dir, snapshotID string) string {
	t.Helper()
	path := filepath.Join(dir, "maintenance", snapshotID, "manager.db")
	database, err := sql.Open("sqlite", db.FileURI(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var name string
	if err := database.QueryRow("SELECT name FROM instances WHERE id='old-world'").Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}
