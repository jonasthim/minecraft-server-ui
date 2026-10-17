package content

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageCleanupRemovesReadOnlyBytesWithoutMakingThemWritable(t *testing.T) {
	f := setup(t, true)
	st, err := f.s.stage(t.Context(), "alpha", admin.ID, "fixture.jar", strings.NewReader("bytes"), 5)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.dirs.State, "content", st.ID+".blob")
	assertReadOnly := func() error {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0200 != 0 {
			t.Fatal("immutable stage became writable before unlink")
		}
		return nil
	}
	if err = assertReadOnly(); err != nil {
		t.Fatal(err)
	}
	observedUnlink := false
	f.s.fault = func(phase string) error {
		if phase != "stage_cleanup_unlink" {
			return nil
		}
		observedUnlink = true
		return assertReadOnly()
	}
	if err = f.s.removeStage(t.Context(), "alpha", st.ID); err != nil {
		t.Fatal(err)
	}
	if !observedUnlink {
		t.Fatal("cleanup did not reach the contained unlink")
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("readonly stage bytes were not removed", err)
	}
	var count int
	if err = f.db.QueryRow("SELECT count(*) FROM content_stages WHERE id=?", st.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("cleanup did not release metadata", count, err)
	}
	f.s.fault = nil
	if err = f.s.removeStage(t.Context(), "alpha", st.ID); err != nil {
		t.Fatal("absent-stage cleanup was not retryable", err)
	}
}

func TestStageCleanupRefusesHardlinkedReadOnlyArtifact(t *testing.T) {
	f := setup(t, true)
	st, err := f.s.stage(t.Context(), "alpha", admin.ID, "fixture.jar", strings.NewReader("bytes"), 5)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.dirs.State, "content", st.ID+".blob")
	alias := filepath.Join(f.dirs.State, "stage-alias.blob")
	if err = os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if err = f.s.removeStage(t.Context(), "alpha", st.ID); err == nil {
		t.Fatal("unsafe hardlinked stage was accepted")
	}
	for _, path := range []string{path, alias} {
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(actual, []byte("bytes")) {
			t.Fatal("refusal changed linked bytes", actual, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0200 != 0 {
			t.Fatal("refusal changed readonly attributes", info, err)
		}
	}
	var count int
	if err = f.db.QueryRow("SELECT count(*) FROM content_stages WHERE id=?", st.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("refusal removed receipt metadata", count, err)
	}
}

func TestStagePublicationFailureReclaimsReadOnlyArtifact(t *testing.T) {
	f := setup(t, true)
	if _, err := f.db.Exec("CREATE TRIGGER fail_stage_publication BEFORE UPDATE OF sha256 ON content_stages BEGIN SELECT RAISE(ABORT,'injected stage publication failure'); END"); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.stage(t.Context(), "alpha", admin.ID, "fixture.jar", strings.NewReader("bytes"), 5)
	if err == nil || !strings.Contains(err.Error(), "injected stage publication failure") {
		t.Fatal("publication failure was not reported", err)
	}
	var count int
	if err = f.db.QueryRow("SELECT count(*) FROM content_stages").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed publication retained quota reservation", count, err)
	}
	entries, err := os.ReadDir(filepath.Join(f.dirs.State, "content"))
	if err != nil || len(entries) != 0 {
		t.Fatal("failed publication retained readonly bytes", entries, err)
	}
}
