package loaderutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type bootstrapContext struct {
	context.Context
	calls  int
	at     int
	change func()
}

func (c *bootstrapContext) Err() error {
	c.calls++
	if c.calls == c.at {
		c.change()
	}
	return nil
}

func bootstrapSubstitute(t *testing.T, original, outside string) func() {
	t.Helper()
	link := original + ".link"
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	return func() {
		if err := os.Rename(original, original+".held"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			os.Remove(original)
			if err := os.Rename(original+".held", original); err != nil {
				t.Error(err)
			}
		})
		if err := os.Rename(link, original); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPublishRejectsAncestorSubstitution(t *testing.T) {
	for _, ancestor := range []string{"data", "instance"} {
		t.Run(ancestor, func(t *testing.T) {
			dir := t.TempDir()
			server := filepath.Join(dir, "instances", "test", "server")
			if err := os.MkdirAll(server, 0700); err != nil {
				t.Fatal(err)
			}
			original := dir
			outside := t.TempDir()
			outsideServer := filepath.Join(outside, "instances", "test", "server")
			if ancestor == "instance" {
				original = filepath.Join(dir, "instances", "test")
				outside = filepath.Join(dir, "instances", "other")
				outsideServer = filepath.Join(outside, "server")
			}
			if err := os.MkdirAll(outsideServer, 0700); err != nil {
				t.Fatal(err)
			}
			runtime := domain.JavaRuntime{ID: "managed", Major: 17, Executable: filepath.Join(dir, "java")}
			publisher := NewPublisher(dir, NewClient(nil))
			publisher.getRuntime = func(context.Context, string) (domain.JavaRuntime, error) { return runtime, nil }
			plan := domain.InstallPlan{Requirements: domain.LaunchRequirements{JavaMajor: 17, MinJavaMajor: 17, MaxJavaMajor: 17}}
			ctx := &bootstrapContext{Context: t.Context(), at: 2, change: bootstrapSubstitute(t, original, outside)}
			built := false
			_, err := publisher.Publish(ctx, "test", plan, runtime, nil, nil, func(context.Context, *os.Root, string, string, bool) ([]string, error) {
				built = true
				return []string{"outside"}, nil
			})
			if err == nil || built {
				t.Fatal("outside builder admitted", built, err)
			}
			if ctx.calls < 2 {
				t.Fatal("substitution window not reached")
			}
			if _, err := os.Stat(filepath.Join(outsideServer, ".manager")); !os.IsNotExist(err) {
				t.Fatal("outside publication mutated", err)
			}
		})
	}
}
func TestPublishRetainsRelativeDataDirectoryNormalization(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "instances", "test", "server"), 0700); err != nil {
		t.Fatal(err)
	}
	runtime := domain.JavaRuntime{ID: "managed", Major: 17, Executable: filepath.Join(dir, "java")}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, dir)
	if err != nil {
		t.Fatal(err)
	}
	publisher := NewPublisher(relative, NewClient(nil))
	publisher.getRuntime = func(context.Context, string) (domain.JavaRuntime, error) { return runtime, nil }
	plan := domain.InstallPlan{Requirements: domain.LaunchRequirements{JavaMajor: 17, MinJavaMajor: 17, MaxJavaMajor: 17}}
	spec, err := publisher.Publish(t.Context(), "test", plan, runtime, nil, nil, func(ctx context.Context, root *os.Root, stage, final string, reuse bool) ([]string, error) {
		if !reuse {
			if err := WriteFile(root, "server.jar", []byte("fixture")); err != nil {
				return nil, err
			}
		}
		return []string{"-jar", filepath.Join(final, "server.jar"), "nogui"}, nil
	})
	if err != nil || !filepath.IsAbs(spec.WorkingDir) {
		t.Fatal("relative configuration regressed", spec, err)
	}
}
func TestVerifyRejectsAncestorSubstitution(t *testing.T) {
	for _, ancestor := range []string{"data", "instance"} {
		t.Run(ancestor, func(t *testing.T) {
			dir := t.TempDir()
			name := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
			if err := os.MkdirAll(filepath.Join(dir, "instances", "test", "server"), 0700); err != nil {
				t.Fatal(err)
			}
			spec := domain.LaunchSpec{WorkingDir: filepath.Join(dir, "instances", "test", "server"), Args: []string{"-jar", filepath.Join(dir, "instances", "test", "server", ".manager", "loaders", name, "server.jar"), "nogui"}}
			original := dir
			outside := t.TempDir()
			target := filepath.Join(outside, "instances", "test", "server", ".manager", "loaders", name)
			if ancestor == "instance" {
				original = filepath.Join(dir, "instances", "test")
				outside = filepath.Join(dir, "instances", "other")
				target = filepath.Join(outside, "server", ".manager", "loaders", name)
			}
			if err := os.MkdirAll(target, 0700); err != nil {
				t.Fatal(err)
			}
			ctx := &bootstrapContext{Context: t.Context(), at: 2, change: bootstrapSubstitute(t, original, outside)}
			err := VerifyLaunch(ctx, dir, "test", spec)
			if err == nil || domain.AsError(err).Code != domain.CodeValidationFailed {
				t.Fatal("verification did not refuse substituted root", err)
			}
			if ctx.calls < 2 {
				t.Fatal("substitution window not reached")
			}
		})
	}
}
