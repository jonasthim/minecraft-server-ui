package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/config"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/managerstate"
)

func TestServeMaintenanceHoldPrecedesDatabaseOpen(t *testing.T) {
	for _, hold := range []string{"hold.json", "hold.pending"} {
		for _, missingDatabase := range []bool{false, true} {
			name := hold + "/existing-database"
			if missingDatabase {
				name = hold + "/missing-database"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				prepareManagerStateCommandTestDirectory(t, dir)
				path := filepath.Join(dir, "manager.db")
				database, err := db.Open(t.Context(), path)
				if err != nil {
					t.Fatal(err)
				}
				if err := database.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := managerstate.Snapshot(t.Context(), dir, version); err != nil {
					t.Fatal(err)
				}
				if err := managerstate.CheckStartup(t.Context(), dir); err != nil {
					t.Fatalf("safe state without hold was refused: %v", err)
				}
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if missingDatabase {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
				holdPath := filepath.Join(dir, "maintenance", hold)
				const holdBytes = "incomplete maintenance must also block startup"
				if err := os.WriteFile(holdPath, []byte(holdBytes), 0600); err != nil {
					t.Fatal(err)
				}
				err = serve(t.Context(), config.Config{DataDir: dir, Address: "invalid-address", BaseURL: "http://127.0.0.1:8080"})
				if err == nil || !strings.Contains(err.Error(), "maintenance review is required") {
					t.Fatalf("serve did not refuse maintenance before startup: %v", err)
				}
				after, err := os.ReadFile(path)
				if missingDatabase {
					if !os.IsNotExist(err) {
						t.Fatalf("startup created a database during maintenance: %v", err)
					}
				} else if err != nil || !bytes.Equal(before, after) {
					t.Fatal("startup changed database bytes during maintenance")
				}
				retained, err := os.ReadFile(holdPath)
				if err != nil || string(retained) != holdBytes {
					t.Fatal("startup changed maintenance hold")
				}
			})
		}
	}
}
