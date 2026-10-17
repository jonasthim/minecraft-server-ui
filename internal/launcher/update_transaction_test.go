package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateMarkerAndWorldRiskIndependentlyBlockBoot(t *testing.T) {
	for _, name := range []string{"update-transaction.json", "update-world-risk.json"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			paths, err := Paths(dir, "alpha")
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(paths.State, 0700); err != nil {
				t.Fatal(err)
			}
			if err = checkUpdateTransactionReady(t.Context(), dir, "alpha"); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(paths.State, name), []byte("malformed orphan marker"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = checkUpdateTransactionReady(t.Context(), dir, "alpha"); err == nil {
				t.Fatal("marker accepted without manager SQL")
			}
		})
	}
}
func TestUpdateReadinessRejectsUnsafeParent(t *testing.T) {
	dir := t.TempDir()
	paths, err := Paths(dir, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(paths.State), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(t.TempDir(), paths.State); err != nil {
		t.Skip("symlink unavailable", err)
	}
	if err = checkUpdateTransactionReady(t.Context(), dir, "alpha"); err == nil {
		t.Fatal("unsafe blocker parent accepted")
	}
}

func TestUpdateHandoffRecoversExactStagedMarkerAndPreservesWorldRisk(t *testing.T) {
	data := t.TempDir()
	paths, err := Paths(data, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(paths.State, 0700); err != nil {
		t.Fatal(err)
	}
	previous := UpdateBlocker{Version: 1, InstanceID: "alpha", TransactionID: strings.Repeat("a", 32), JobID: strings.Repeat("b", 32), Kind: "update"}
	next := UpdateBlocker{Version: 1, InstanceID: "alpha", TransactionID: strings.Repeat("c", 32), JobID: strings.Repeat("d", 32), Kind: "instance_rollback"}
	if err = WriteUpdateBlocker(t.Context(), data, previous); err != nil {
		t.Fatal(err)
	}
	if err = WriteWorldRiskBlocker(t.Context(), data, previous); err != nil {
		t.Fatal(err)
	}
	staged, _ := json.Marshal(next)
	if err = os.WriteFile(filepath.Join(paths.State, "update-handoff.tmp"), staged, 0600); err != nil {
		t.Fatal(err)
	}
	if err = TransferUpdateBlocker(t.Context(), data, previous, next); err != nil {
		t.Fatal(err)
	}
	if err = TransferUpdateBlocker(t.Context(), data, previous, next); err != nil {
		t.Fatal("exact replay failed", err)
	}
	actual, present, err := ReadUpdateBlocker(t.Context(), data, "alpha")
	if err != nil || !present || actual != next {
		t.Fatal("successor marker not active", err)
	}
	risk, present, err := ReadWorldRiskBlocker(t.Context(), data, "alpha")
	if err != nil || !present || risk != previous {
		t.Fatal("world risk evidence lost", err)
	}
	if err = CheckActivationReady(t.Context(), data, "alpha"); err == nil {
		t.Fatal("independent activation bypassed handoff markers")
	}
	foreign := previous
	foreign.TransactionID = strings.Repeat("e", 32)
	if err = ClearUpdateBlocker(t.Context(), data, foreign); err == nil {
		t.Fatal("foreign marker cleared")
	}
}
