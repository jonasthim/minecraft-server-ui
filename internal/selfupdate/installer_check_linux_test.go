//go:build linux

package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInspectLinuxInstallerStateIsReadOnlyAcrossStateRelations(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(*testing.T, linuxInstallerHooks, nativePaths)
		want    Installed
		present bool
		pending bool
		wantErr bool
	}{
		{
			name: "absent",
			prepare: func(t *testing.T, _ linuxInstallerHooks, p nativePaths) {
				if err := os.Remove(p.State); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "installed",
			prepare: func(t *testing.T, _ linuxInstallerHooks, p nativePaths) {
				image := []byte("manager image")
				if err := os.WriteFile(filepath.Join(p.Bin, p.Binary), image, 0755); err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(image)
				installed := Installed{Format: 1, Version: "v1.2.3", Schema: 4, SHA256: hex.EncodeToString(sum[:])}
				writeLinuxInspectionJSON(t, filepath.Join(p.State, "installed.json"), installed)
			},
			want:    inspectionReceiptForBytes("v1.2.3", []byte("manager image")),
			present: true,
		},
		{
			name: "terminal healthy snapshot",
			prepare: func(t *testing.T, _ linuxInstallerHooks, p nativePaths) {
				prepareLinuxTerminalFixture(t, p, "healthy", "candidate", false)
			},
			want:    inspectionFixtureInstalled("candidate"),
			present: true,
		},
		{
			name: "nonterminal activation",
			prepare: func(t *testing.T, _ linuxInstallerHooks, p nativePaths) {
				writeLinuxInspectionJSON(t, filepath.Join(p.State, "activation.json"), map[string]any{"format": 1})
			},
			pending: true,
			wantErr: true,
		},
		{
			name: "orphan manager control without state root",
			prepare: func(t *testing.T, _ linuxInstallerHooks, p nativePaths) {
				if err := os.Remove(p.State); err != nil {
					t.Fatal(err)
				}
				control := controlDirectory(p.Data)
				if err := os.MkdirAll(control, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(control, "request.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			},
			pending: true,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, paths := linuxInstallerFixture(t)
			tc.prepare(t, h, paths)
			before := snapshotInspectionTree(t, filepath.Dir(paths.State))
			got, present, pending, err := inspectLinuxInstallerState(context.Background(), h)
			after := snapshotInspectionTree(t, filepath.Dir(paths.State))
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("read-only inspection changed its fixture tree:\nbefore=%v\nafter=%v", before, after)
			}
			if (err != nil) != tc.wantErr || got != tc.want || present != tc.present || pending != tc.pending {
				t.Fatalf("inspection=(%+v,%v,%v,%v), want=(%+v,%v,%v,err=%v)", got, present, pending, err, tc.want, tc.present, tc.pending, tc.wantErr)
			}
		})
	}
}

func TestInspectLinuxInstallerStateChecksAuthorityBeforePathIO(t *testing.T) {
	h, paths := linuxInstallerFixture(t)
	called := false
	h.authorized = func() error { return errors.New("denied") }
	h.openProtectedRoot = func(context.Context, string) (*os.Root, error) {
		called = true
		return nil, errors.New("unexpected root open")
	}
	if _, _, _, err := inspectLinuxInstallerState(context.Background(), h); err == nil {
		t.Fatal("inspection without authority succeeded")
	}
	if called {
		t.Fatal("path I/O happened before authority check")
	}
	if _, err := os.Stat(paths.State); err != nil {
		t.Fatalf("fixture state unexpectedly changed: %v", err)
	}
}

func inspectionFixtureInstalled(which string) Installed {
	var image []byte
	version := "v1.2.3"
	if which == "candidate" {
		image = []byte("candidate executable image")
	} else {
		image = []byte("prior executable image")
		version = "v1.2.2"
	}
	return inspectionReceiptForBytes(version, image)
}

func inspectionReceiptForBytes(version string, image []byte) Installed {
	sum := sha256.Sum256(image)
	return Installed{Format: 1, Version: version, Schema: 4, SHA256: hex.EncodeToString(sum[:])}
}

func writeLinuxInspectionJSON(t *testing.T, path string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func snapshotInspectionTree(t *testing.T, root string) map[string]string {
	t.Helper()
	entries := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if !entry.IsDir() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += ":" + string(body)
		}
		entries[rel] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
