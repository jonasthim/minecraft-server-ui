//go:build linux

package selfupdate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestEnsureLinuxUpdaterStagesRoot(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0700); err != nil {
		t.Fatal(err)
	}
	state, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	t.Run("creates only missing private stages directory", func(t *testing.T) {
		stages, err := ensureLinuxUpdaterStagesRootWithOwner(context.Background(), state, rootPath, uint32(os.Geteuid()), uint32(os.Getegid()))
		if err != nil {
			t.Fatal(err)
		}
		if err := stages.Close(); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(filepath.Join(rootPath, "stages"))
		if err != nil {
			t.Fatal(err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || stat.Uid != uint32(os.Geteuid()) || stat.Gid != uint32(os.Getegid()) {
			t.Fatalf("created stages root metadata = mode %v stat %+v", info.Mode(), stat)
		}
	})

	t.Run("preserves existing exact private directory and contents", func(t *testing.T) {
		marker := filepath.Join(rootPath, "stages", "existing-stage")
		if err := os.WriteFile(marker, []byte("retained"), 0600); err != nil {
			t.Fatal(err)
		}
		stages, err := ensureLinuxUpdaterStagesRootWithOwner(context.Background(), state, rootPath, uint32(os.Geteuid()), uint32(os.Getegid()))
		if err != nil {
			t.Fatal(err)
		}
		if err := stages.Close(); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(marker)
		if err != nil || string(body) != "retained" {
			t.Fatalf("existing stage content = %q, %v", body, err)
		}
	})
}

func TestPrivilegedUpdaterCanStageAfterLazyProtectedDirectoryCreation(t *testing.T) {
	statePath := t.TempDir()
	if err := os.Chmod(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	state, err := os.OpenRoot(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	stagesPath := filepath.Join(statePath, "stages")
	if _, err := os.Lstat(stagesPath); !os.IsNotExist(err) {
		t.Fatalf("test requires absent initial stages root, got %v", err)
	}
	archive := archiveFixture(t, "linux", []string{"minecraft-ui", "LICENSE", "THIRD_PARTY_NOTICES.txt"})
	trust, manifest, signature := stageFixture(t, "linux", archive)
	stager := Stager{Dir: stagesPath, Trust: trust, CurrentVersion: "v1.2.2", Schema: 16, OS: "linux", Arch: "amd64", Now: func() time.Time { return fixtureNow }}
	if _, err := stager.Stage(context.Background(), "v1.2.3", manifest, signature, bytes.NewReader(archive)); err == nil {
		t.Fatal("stager unexpectedly created the missing protected directory itself")
	}

	stages, err := ensureLinuxUpdaterStagesRootWithOwner(context.Background(), state, statePath, uint32(os.Geteuid()), uint32(os.Getegid()))
	if err != nil {
		t.Fatal(err)
	}
	if err := stages.Close(); err != nil {
		t.Fatal(err)
	}
	stage, err := stager.Stage(context.Background(), "v1.2.3", manifest, signature, bytes.NewReader(archive))
	if err != nil || stage.ID == "" {
		t.Fatalf("privileged updater stage after lazy creation = %+v, %v", stage, err)
	}
}

func TestEnsureLinuxUpdaterStagesRootRejectsUnsafeExistingDirectory(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(string) error
	}{
		{name: "wrong mode", setup: func(path string) error { return os.Mkdir(path, 0750) }},
		{name: "regular file", setup: func(path string) error { return os.WriteFile(path, []byte("foreign"), 0600) }},
		{name: "symlink", setup: func(path string) error { return os.Symlink("/tmp", path) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rootPath := t.TempDir()
			if err := os.Chmod(rootPath, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(rootPath, "stages")
			if err := tc.setup(path); err != nil {
				t.Fatal(err)
			}
			state, err := os.OpenRoot(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			stages, err := ensureLinuxUpdaterStagesRootWithOwner(context.Background(), state, rootPath, uint32(os.Geteuid()), uint32(os.Getegid()))
			if err == nil {
				_ = stages.Close()
				t.Fatal("accepted unsafe existing stages path")
			}
		})
	}
}

func TestEnsureLinuxUpdaterStagesRootRequiresCurrentStateBinding(t *testing.T) {
	rootPath := t.TempDir()
	replacementPath := t.TempDir()
	if err := os.Chmod(rootPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(replacementPath, 0700); err != nil {
		t.Fatal(err)
	}
	state, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := os.Rename(rootPath, rootPath+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacementPath, rootPath); err != nil {
		t.Fatal(err)
	}
	stages, err := ensureLinuxUpdaterStagesRootWithOwner(context.Background(), state, rootPath, uint32(os.Geteuid()), uint32(os.Getegid()))
	if err == nil {
		_ = stages.Close()
		t.Fatal("accepted replaced state root")
	}
}
