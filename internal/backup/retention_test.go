package backup

import (
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"reflect"
	"testing"
	"time"
)

func TestRetentionProtectsManualSafetyAndBothAutomaticLimits(t *testing.T) {
	now := time.Now()
	backups := []domain.Backup{}
	for i := 0; i < 12; i++ {
		backups = append(backups, domain.Backup{ID: string(rune('a' + i)), Trigger: domain.BackupAutomatic, State: domain.BackupReady, CreatedAt: now.Add(-time.Duration(i) * time.Hour)})
	}
	backups = append(backups, domain.Backup{ID: "manual", Trigger: domain.BackupManual, Protected: true, State: domain.BackupReady, CreatedAt: now.Add(-1000 * time.Hour)}, domain.Backup{ID: "safety", Trigger: domain.BackupSafety, Protected: true, State: domain.BackupReady, CreatedAt: now.Add(-1000 * time.Hour)}, domain.Backup{ID: "expired", Trigger: domain.BackupAutomatic, State: domain.BackupReady, CreatedAt: now.Add(-1000 * time.Hour)})
	got := retentionCandidates(backups, DefaultRetention, now)
	if len(got) != 3 || got[0] != "k" || got[1] != "l" || got[2] != "expired" {
		t.Fatalf("retention %v", got)
	}
}

func TestRetentionDisabledLimitsAndProtection(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	backups := []domain.Backup{
		{ID: "new", Trigger: domain.BackupAutomatic, State: domain.BackupReady, CreatedAt: now},
		{ID: "old", Trigger: domain.BackupAutomatic, State: domain.BackupReady, CreatedAt: now.Add(-48 * time.Hour)},
		{ID: "manual", Trigger: domain.BackupManual, State: domain.BackupReady, CreatedAt: now.Add(-48 * time.Hour)},
		{ID: "safety", Trigger: domain.BackupSafety, State: domain.BackupReady, CreatedAt: now.Add(-48 * time.Hour)},
		{ID: "protected", Trigger: domain.BackupAutomatic, Protected: true, State: domain.BackupReady, CreatedAt: now.Add(-48 * time.Hour)},
		{ID: "failed", Trigger: domain.BackupAutomatic, State: domain.BackupFailed, CreatedAt: now.Add(-48 * time.Hour)},
	}
	for _, tc := range []struct {
		name   string
		policy Retention
		want   []string
	}{
		{"count only", Retention{Count: 1}, []string{"old"}},
		{"age only", Retention{MaxAge: 24 * time.Hour}, []string{"old"}},
		{"both", Retention{Count: 1, MaxAge: 24 * time.Hour}, []string{"old"}},
		{"unlimited", Retention{}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retentionCandidates(backups, tc.policy, now); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
