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
	"syscall"

	"github.com/jonasthim/minecraft-server-ui/release/bootstrap"
)

var version = "0.0.0-dev"

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fail := func(message string) int { fmt.Fprintln(stderr, message); return 1 }
	if len(args) == 1 && args[0] == "version" {
		if _, err := fmt.Fprintln(stdout, "minecraft-ui-bootstrap "+version); err != nil {
			return fail("bootstrap version output failed")
		}
		return 0
	}
	if len(args) == 0 || args[0] != "prepare" {
		return fail("usage: minecraft-ui-bootstrap prepare --repository OWNER/REPO --trust-file ABSFILE --version latest|vMAJOR.MINOR.PATCH --output ABSNEWPATH")
	}
	var o bootstrap.Options
	f := flag.NewFlagSet("prepare", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.Repository, "repository", "", "public GitHub repository")
	f.StringVar(&o.TrustFile, "trust-file", "", "independently supplied absolute public trust file")
	f.StringVar(&o.Version, "version", "", "latest or exact stable version")
	f.StringVar(&o.Output, "output", "", "new absolute private output directory")
	f.StringVar(&o.Artifacts, "artifacts", "", "optional local candidate directory for an exact version")
	f.BoolVar(&o.AllowDevelopment, "allow-development", false, "explicitly permit authenticated development inputs")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 {
		return fail("invalid bootstrap command arguments")
	}
	o.OS = runtime.GOOS
	o.Arch = runtime.GOARCH
	r, err := bootstrap.Prepare(ctx, o, nil)
	if err != nil {
		return fail(err.Error())
	}
	if err = json.NewEncoder(stdout).Encode(r); err != nil {
		return fail("bootstrap receipt output failed")
	}
	return 0
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
