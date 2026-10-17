package update

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// Exercise receipt reclamation after a real composed update and then consume its
// independent recovery material. Native installation/sealing remain fixtures.
func TestCompletedUpdateReclaimsReceiptsAndKeepsRecoveryMaterial(t *testing.T) {
	f, request, body := manualContentFixture(t)
	upload, err := f.s.config.Content.StageUpload(t.Context(), "alpha", "example.jar", bytes.NewReader(body), f.actor)
	if err != nil {
		t.Fatal(err)
	}
	request.ContentManual = map[string]string{"example": upload.ID}
	ready := f.previewRequest(t, request)
	if len(ready.Blockers) > 0 {
		t.Fatal(ready.Blockers)
	}
	job, err := f.s.EnqueueApply(t.Context(), "alpha", ready.ID, true, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if done := f.wait(t, job); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
	var transaction, raw string
	if err = f.s.config.DB.QueryRow("SELECT id,journal_json FROM update_transactions WHERE state='complete'").Scan(&transaction, &raw); err != nil {
		t.Fatal(err)
	}
	journal, err := decodeJournal(raw, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Operations) == 0 {
		t.Fatal("fixture did not create independent content recovery operations")
	}
	backups, err := f.s.config.Backups.List(t.Context(), "alpha")
	if err != nil || len(backups) != 1 {
		t.Fatal(backups, err)
	}
	// Expiry is only simulated for temporary acquisition artifacts and previews.
	// SQL history, safety backup, selected bytes and recovery blobs stay real.
	if _, err = f.s.config.DB.Exec("UPDATE content_stages SET created_at='2000-01-01T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.config.DB.Exec("UPDATE update_previews SET expires_at='2000-01-01T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.config.Content.StageUpload(t.Context(), "alpha", "next.jar", bytes.NewReader(body), f.actor); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = f.s.config.DB.QueryRow("SELECT count(*) FROM content_stages WHERE created_at='2000-01-01T00:00:00Z'").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("completed update permanently pinned receipts", remaining, err)
	}
	if _, err = os.Stat(filepath.Join(f.dirs.State, "content", upload.ID+".blob")); !os.IsNotExist(err) {
		t.Fatal("old upload bytes retained", err)
	}
	var after string
	if err = f.s.config.DB.QueryRow("SELECT journal_json FROM update_transactions WHERE id=?", transaction).Scan(&after); err != nil || after != raw {
		t.Fatal("immutable transaction evidence changed", err)
	}
	root, err := f.s.journalRoot(t.Context(), "alpha", transaction, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, operation := range journal.Operations {
		for _, blob := range []struct{ name, hash string }{{operation.BeforeBlob, operation.Before}, {operation.AfterBlob, operation.After}} {
			if blob.name == "" {
				continue
			}
			actual, err := content.PackCurrentHash(t.Context(), root, blob.name, content.MaxArtifactBytes)
			if err != nil || actual != blob.hash {
				t.Fatal("independent recovery bytes changed", blob.name, actual, err)
			}
		}
	}
	retained, err := f.s.config.Backups.List(t.Context(), "alpha")
	if err != nil || !reflect.DeepEqual(retained, backups) {
		t.Fatal("protected backup metadata changed", err)
	}
	archive, err := f.s.config.Backups.Download(t.Context(), "alpha", backups[0].ID, f.actor)
	if err != nil {
		t.Fatal("independent rollback backup is no longer verifiable", err)
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(f.dirs.Server, "mods/example.jar"))
	if err != nil || !bytes.Equal(actual, body) {
		t.Fatal("cleanup changed active content", err)
	}
	// A later preboot reconciliation uses the copied before/after blobs, never
	// the now removed publisher/manual receipts. This simulates interrupted work
	// with a committed generation and must leave the selected NEW content intact.
	if err = f.s.setJournalState(t.Context(), transaction, journal, "committed"); err != nil {
		t.Fatal(err)
	}
	f.pauseRecovery(t)
	if err = f.s.Reconcile(t.Context()); err != nil {
		t.Fatal("recovery required reclaimed temporary receipts", err)
	}
	actual, err = os.ReadFile(filepath.Join(f.dirs.Server, "mods/example.jar"))
	if err != nil || !bytes.Equal(actual, body) {
		t.Fatal("reconciliation changed selected content", err)
	}
}
