package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/install"
	"github.com/jonasthim/minecraft-server-ui/release/bootstrap"
)

var version = "0.0.0-dev"

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runWithBackend(ctx, args, stdout, stderr, bootstrapBackend{
		prepare: func(ctx context.Context, o bootstrap.Options) (bootstrap.Receipt, error) {
			return bootstrap.Prepare(ctx, o, nil)
		},
		installer:  install.RunInstallerCommand,
		canInstall: func() bool { return runtime.GOOS == "linux" && runtime.GOARCH == "amd64" && os.Geteuid() == 0 },
	})
}

type bootstrapBackend struct {
	prepare    func(context.Context, bootstrap.Options) (bootstrap.Receipt, error)
	installer  func(context.Context, []string, io.Writer) (bool, error)
	canInstall func() bool
}

func runWithBackend(ctx context.Context, args []string, stdout, stderr io.Writer, backend bootstrapBackend) int {
	fail := func(message string) int { fmt.Fprintln(stderr, message); return 1 }
	if len(args) == 1 && args[0] == "version" {
		if _, err := fmt.Fprintln(stdout, "minecraft-ui-bootstrap "+version); err != nil {
			return fail("bootstrap version output failed")
		}
		return 0
	}
	if handled, err := backend.installer(ctx, args, stdout); handled || err != nil {
		if err != nil {
			return fail("Linux installer operation failed; inspect install check or recover")
		}
		return 0
	}
	if len(args) == 0 || args[0] != "prepare" {
		return fail("usage: minecraft-ui-bootstrap prepare --repository OWNER/REPO --trust-file ABSFILE --version latest|vMAJOR.MINOR.PATCH --output ABSNEWPATH [--install [--repair]]; or install apply|check|uninstall|recover")
	}
	var o bootstrap.Options
	var apply, repair bool
	f := flag.NewFlagSet("prepare", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.Repository, "repository", "", "public GitHub repository")
	f.StringVar(&o.TrustFile, "trust-file", "", "independently supplied absolute public trust file")
	f.StringVar(&o.Version, "version", "", "latest or exact stable version")
	f.StringVar(&o.Output, "output", "", "new absolute private output directory")
	f.StringVar(&o.Artifacts, "artifacts", "", "optional local candidate directory for an exact version")
	f.BoolVar(&o.AllowDevelopment, "allow-development", false, "explicitly permit authenticated development inputs")
	f.BoolVar(&apply, "install", false, "install authenticated inputs on this Linux host")
	f.BoolVar(&repair, "repair", false, "repair missing owned deployment assets during installation")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 {
		return fail("invalid bootstrap command arguments")
	}
	seen := make(map[string]bool)
	for _, arg := range args[1:] {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if seen[name] {
			return fail("duplicate bootstrap command argument")
		}
		seen[name] = true
	}
	if repair && !apply {
		return fail("repair requires install")
	}
	if apply && (!backend.canInstall() || o.Repository != "jonasthim/minecraft-server-ui") {
		return fail("installation requires Linux x64 administrator authority and the canonical repository")
	}
	o.OS = runtime.GOOS
	o.Arch = runtime.GOARCH
	r, err := backend.prepare(ctx, o)
	if err != nil {
		return fail(err.Error())
	}
	if apply {
		// Use the resolved authenticated version, including when the caller
		// selected latest. The privileged installer imports and re-verifies the
		// private prepared inputs before any service or deployment changes.
		command := []string{"install", "apply", "--prepared", o.Output, "--trust-file", o.TrustFile, "--version", r.Version}
		if repair {
			command = append(command, "--repair")
		}
		if o.AllowDevelopment {
			command = append(command, "--allow-development")
		}
		if handled, err := backend.installer(ctx, command, stdout); !handled || err != nil {
			return fail("Linux installer operation failed; inspect install check or recover")
		}
		return 0
	}
	if err = json.NewEncoder(stdout).Encode(r); err != nil {
		return fail("bootstrap receipt output failed")
	}
	return 0
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, limit := context.WithTimeout(ctx, 20*time.Minute)
	defer limit()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
