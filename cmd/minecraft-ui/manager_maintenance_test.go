package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
)

func managerMaintenanceChallenge(t *testing.T, dir string, local bool) string {
	t.Helper()
	control := filepath.Join(dir, "selfupdate", "control")
	if err := os.MkdirAll(control, 0700); err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("a", 64)
	var value any
	name := "challenge.json"
	if local {
		hash, err := selfupdate.ExecutableSHA256(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		image := selfupdate.Installed{Format: 1, Version: "v1.2.3", Schema: db.LatestVersion(), SHA256: hash}
		value = selfupdate.MaintenanceChallenge{Format: 1, OperationID: strings.Repeat("b", 64), Nonce: nonce, Mode: "apply", From: &image, To: &image, ExpiresAt: time.Now().Add(time.Minute)}
		name = "maintenance-challenge.json"
	} else {
		value = selfupdate.Challenge{Format: 1, Nonce: nonce, FromVersion: "v1.2.3", ToVersion: "v1.2.3", ActorID: 1, ExpiresAt: time.Now().Add(time.Minute)}
	}
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(control, name), body, 0600); err != nil {
		t.Fatal(err)
	}
	return nonce
}

func TestManagerClosePublishesOnlyRequestedDrainAfterDBCloseWhileLockHeld(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(map[bool]string{false: "web", true: "maintenance"}[local], func(t *testing.T) {
			dir := t.TempDir()
			release, err := launcher.AcquireManager(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			database, err := db.Open(t.Context(), filepath.Join(dir, "manager.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			managerMaintenanceChallenge(t, dir, local)
			if err := closeManagerDatabase(t.Context(), database, dir, "v1.2.3", nil); err != nil {
				t.Fatal(err)
			}
			if err := database.PingContext(t.Context()); err == nil {
				t.Fatal("drain published with database still open")
			}
			if unlock, err := launcher.AcquireManager(dir); err == nil {
				unlock()
				t.Fatal("drain released data lock before caller")
			}
			expected, other := "drained.json", "maintenance-drained.json"
			if local {
				expected, other = other, expected
			}
			control := filepath.Join(dir, "selfupdate", "control")
			body, err := os.ReadFile(filepath.Join(control, expected))
			if err != nil {
				t.Fatal(err)
			}
			var proof struct {
				Drained bool `json:"drained"`
			}
			if json.Unmarshal(body, &proof) != nil || !proof.Drained {
				t.Fatal("missing drain proof")
			}
			if _, err := os.Lstat(filepath.Join(control, other)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("wrong protocol published", err)
			}
			if _, err := os.Lstat(filepath.Join(control, "maintenance-drained.pending")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("pending receipt remains", err)
			}
		})
	}
}

func TestManagerCloseFailureOrAmbiguousChallengeNeverPublishesProof(t *testing.T) {
	for _, mode := range []string{"earlier-error", "both-challenges", "closed-db"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			database, err := db.Open(t.Context(), filepath.Join(dir, "manager.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			managerMaintenanceChallenge(t, dir, true)
			var original error
			if mode == "earlier-error" {
				original = context.Canceled
			}
			if mode == "both-challenges" {
				managerMaintenanceChallenge(t, dir, false)
			}
			if mode == "closed-db" {
				if err := database.Close(); err != nil {
					t.Fatal(err)
				}
			}
			err = closeManagerDatabase(t.Context(), database, dir, "v1.2.3", original)
			if err == nil || original != nil && !errors.Is(err, original) {
				t.Fatal("unsafe shutdown reported clean", err)
			}
			if err := database.PingContext(t.Context()); err == nil {
				t.Fatal("failed preparation left database open")
			}
			for _, name := range []string{"drained.json", "maintenance-drained.json", "maintenance-drained.pending"} {
				if _, err := os.Lstat(filepath.Join(dir, "selfupdate", "control", name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("unsafe shutdown published proof", name, err)
				}
			}
		})
	}
}

func TestManagerMountsSeparateMaintenanceReadinessWithSharedAdmission(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(map[bool]string{false: "web", true: "maintenance"}[local], func(t *testing.T) {
			dir := t.TempDir()
			nonce := managerMaintenanceChallenge(t, dir, local)
			ready := false
			r := chi.NewRouter()
			mountManagerReadiness(r, dir, "v1.2.3", db.LatestVersion(), func() bool { return ready })
			wanted, other := "/internal/update-ready", "/internal/maintenance-ready"
			if local {
				wanted, other = other, wanted
			}
			request := func(path string) int {
				req := httptest.NewRequest("GET", path, nil)
				req.RemoteAddr = "127.0.0.1:12000"
				req.Header.Set("Authorization", "Bearer "+nonce)
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				return w.Code
			}
			if got := request(wanted); got != 503 {
				t.Fatal("startup admission bypassed", got)
			}
			ready = true
			if got := request(other); got != 403 {
				t.Fatal("nonce crossed protocol", got)
			}
			if got := request(wanted); got != 200 {
				t.Fatal("ready proof unavailable", got)
			}
			if got := request(wanted); got != 403 {
				t.Fatal("ready proof replayed", got)
			}
		})
	}
}
