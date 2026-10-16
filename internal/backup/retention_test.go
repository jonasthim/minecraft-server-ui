package backup

import (
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
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
