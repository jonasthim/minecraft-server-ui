package backup

import (
	"encoding/json"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"testing"
)

func TestScheduledBackupFixedAutomaticTriggerAndReplay(t *testing.T) {
	s, _, _ := fixture(t, false)
	first, e := s.EnqueueAutomaticOnce(t.Context(), "schedule:backup", "alpha", domain.BackupWorld, testActor)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.EnqueueAutomaticOnce(t.Context(), "schedule:backup", "alpha", domain.BackupWorld, testActor)
	if e != nil || first.ID != again.ID {
		t.Fatal(first, again, e)
	}
	var raw string
	if e = s.db.QueryRow(`SELECT payload FROM jobs WHERE id=?`, first.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var in createRequest
	if e = json.Unmarshal([]byte(raw), &in); e != nil || in.Trigger != domain.BackupAutomatic || in.Kind != domain.BackupWorld {
		t.Fatal(in, e)
	}
	if _, e = s.EnqueueAutomaticOnce(t.Context(), "", "alpha", domain.BackupWorld, testActor); e == nil {
		t.Fatal("missing key admitted")
	}
}
