package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/config"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

func TestDuplicateManagerCannotRecoverAnotherManagersJobs(t *testing.T) {
	root := t.TempDir()
	release, err := launcher.AcquireManager(root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	database, err := db.Open(context.Background(), filepath.Join(root, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_, err = database.Exec(`INSERT INTO jobs(id,kind,state,phase,created_at,started_at) VALUES('first-manager-running','test','running','working','2026-10-16T00:00:00Z','2026-10-16T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	err = serve(context.Background(), config.Config{DataDir: root, Address: "invalid-address", BaseURL: "http://127.0.0.1:8080"})
	if err == nil || !strings.Contains(err.Error(), "another manager") {
		t.Fatalf("second manager was not rejected before opening database: %v", err)
	}
	var state string
	if err = database.QueryRow(`SELECT state FROM jobs WHERE id='first-manager-running'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "running" {
		t.Fatal("second manager changed first manager's live job", state)
	}
}
