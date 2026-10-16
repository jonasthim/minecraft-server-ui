package providerutil

import (
	"context"
	"io"
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
			i, plan, r := setup(t, jar(t, false))
			original := i.dataDir
			outside := t.TempDir()
			server := filepath.Join(outside, "instances", "test", "server")
			if ancestor == "instance" {
				original = filepath.Join(i.dataDir, "instances", "test")
				outside = filepath.Join(i.dataDir, "instances", "other")
				server = filepath.Join(outside, "server")
			}
			if err := os.MkdirAll(server, 0700); err != nil {
				t.Fatal(err)
			}
			ctx := &bootstrapContext{Context: t.Context(), at: 2, change: bootstrapSubstitute(t, original, outside)}
			if _, err := i.Publish(ctx, "test", plan, r, nil, io.Discard); err == nil {
				t.Fatal("outside publication admitted")
			}
			if ctx.calls < 2 {
				t.Fatal("substitution window not reached")
			}
			if _, err := os.Stat(filepath.Join(server, ".manager")); !os.IsNotExist(err) {
				t.Fatal("outside publication mutated", err)
			}
		})
	}
}
func TestPublishRetainsRelativeDataDirectoryNormalization(t *testing.T) {
	i, plan, r := setup(t, jar(t, false))
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	i.dataDir, err = filepath.Rel(cwd, i.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := i.Publish(t.Context(), "test", plan, r, nil, io.Discard)
	if err != nil || !filepath.IsAbs(spec.WorkingDir) {
		t.Fatal("relative configuration regressed", spec, err)
	}
}
func TestVerifyRejectsAncestorSubstitution(t *testing.T) {
	for _, ancestor := range []string{"data", "instance"} {
		t.Run(ancestor, func(t *testing.T) {
			i, plan, r := setup(t, jar(t, false))
			spec, err := i.Publish(t.Context(), "test", plan, r, nil, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			original := i.dataDir
			outside := t.TempDir()
			if ancestor == "instance" {
				original = filepath.Join(i.dataDir, "instances", "test")
				outside = filepath.Join(i.dataDir, "instances", "other")
			}
			if err = os.CopyFS(outside, os.DirFS(original)); err != nil {
				t.Fatal(err)
			}
			ctx := &bootstrapContext{Context: t.Context(), at: 2, change: bootstrapSubstitute(t, original, outside)}
			err = VerifyLaunch(ctx, i.dataDir, "test", spec)
			if err == nil || domain.AsError(err).Code != domain.CodeValidationFailed {
				t.Fatal("verification did not refuse substituted root", err)
			}
			if ctx.calls < 2 {
				t.Fatal("substitution window not reached")
			}
		})
	}
}
