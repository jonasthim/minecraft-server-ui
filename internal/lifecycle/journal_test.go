package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// These tests must fail if recovery follows file state rather than the durable
// DB generation, or if activation destroys an earlier verified generation.
func TestActivationRecoveryUsesCommittedGeneration(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_commit", true: "after_commit"}[committed], func(t *testing.T) {
			ctx := context.Background()
			database, err := db.OpenMemory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			_, err = database.Exec(`INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port) VALUES('test','Test','vanilla','1.20.1',17,1024,25565); INSERT INTO instance_settings(instance_id) VALUES('test')`)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			state := filepath.Join(dir, "instances", "test", "state")
			if err = os.MkdirAll(state, 0700); err != nil {
				t.Fatal(err)
			}
			old := installation{Generation: "old", Spec: domain.LaunchSpec{JavaRuntimeID: "old-runtime"}}
			next := installation{Generation: "next", Spec: domain.LaunchSpec{JavaRuntimeID: "next-runtime"}}
			s := &Service{db: database, dataDir: dir}
			var sealed []string
			s.seal = func(_ context.Context, _ string, spec domain.LaunchSpec) error {
				sealed = append(sealed, spec.JavaRuntimeID)
				return nil
			}
			if err = s.saveInstallation(ctx, "test", old); err != nil {
				t.Fatal(err)
			}
			if err = s.intent(ctx, "test", next); err != nil {
				t.Fatal(err)
			}
			if committed {
				if err = s.saveInstallation(ctx, "test", next); err != nil {
					t.Fatal(err)
				}
			}
			if err = s.recoverActivation(ctx, "test"); err != nil {
				t.Fatal(err)
			}
			expected := "old-runtime"
			if committed {
				expected = "next-runtime"
			}
			if len(sealed) != 1 || sealed[0] != expected {
				t.Fatalf("sealed=%v want %s", sealed, expected)
			}
			var n int
			if err = database.QueryRow(`SELECT COUNT(*) FROM lifecycle_activations`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("pending=%d err=%v", n, err)
			}
			var retained int
			if err = database.QueryRow(`SELECT COUNT(*) FROM lifecycle_generations`).Scan(&retained); err != nil || retained < 1 {
				t.Fatalf("retained=%d err=%v", retained, err)
			}
		})
	}
}
func TestMemoryBindingRejectsProviderFlags(t *testing.T) {
	for _, args := range [][]string{{"-Xmx9G", "-jar", "x"}, {"-Xms256M", "-Xmx1024M", "-jar", "x"}} {
		_, err := bindMemory(domain.LaunchSpec{Args: args}, 2048)
		if err == nil {
			t.Fatalf("accepted mismatched memory %v", args)
		}
	}
	spec, err := bindMemory(domain.LaunchSpec{Args: []string{"-jar", "x", "nogui"}}, 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Args) != 5 || spec.Args[0] != "-Xms256M" || spec.Args[1] != "-Xmx256M" {
		t.Fatalf("args=%v", spec.Args)
	}
}

func TestActivationFailureBeforeAndAfterSealRestoresCommittedBytes(t *testing.T) {
	for _, afterSeal := range []bool{false, true} {
		t.Run(map[bool]string{false: "seal_failure", true: "commit_failure"}[afterSeal], func(t *testing.T) {
			s, _, _ := fixture(t)
			ctx := context.Background()
			old := installation{Generation: "old", Spec: domain.LaunchSpec{JavaRuntimeID: "old-runtime"}}
			next := installation{Generation: "next", Spec: domain.LaunchSpec{JavaRuntimeID: "new-runtime"}}
			if err := s.saveInstallation(ctx, "test", old); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.dataDir, "instances", "test", "state", "launch.json")
			if err := os.WriteFile(path, []byte("old-runtime"), 0600); err != nil {
				t.Fatal(err)
			}
			s.seal = func(_ context.Context, _ string, spec domain.LaunchSpec) error {
				if !afterSeal && spec.JavaRuntimeID == "new-runtime" {
					return errors.New("interrupted before replacement")
				}
				return os.WriteFile(path, []byte(spec.JavaRuntimeID), 0600)
			}
			if afterSeal {
				if _, err := s.db.Exec(`CREATE TRIGGER reject_activation BEFORE UPDATE ON lifecycle_installations BEGIN SELECT RAISE(ABORT,'injected commit failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.activate(ctx, "test", next); err == nil {
				t.Fatal("injected failure not observed")
			}
			current, err := s.loadInstallation(ctx, "test")
			if err != nil || current.Generation != "old" {
				t.Fatalf("committed generation changed: %+v %v", current, err)
			}
			if err = s.recoverActivation(ctx, "test"); err != nil {
				t.Fatal(err)
			}
			bytes, err := os.ReadFile(path)
			if err != nil || string(bytes) != "old-runtime" {
				t.Fatalf("old activation not recovered: %s %v", bytes, err)
			}
			var pending int
			if err = s.db.QueryRow(`SELECT COUNT(*) FROM lifecycle_activations`).Scan(&pending); err != nil || pending != 0 {
				t.Fatalf("pending=%d %v", pending, err)
			}
		})
	}
}
