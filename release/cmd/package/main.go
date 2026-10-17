// Command package prepares signed local release candidates. It never publishes
// releases, modifies trust, installs services, or creates production keys.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if e := run(ctx, os.Args[1:], os.Stdin, os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, "candidate packaging refused:", e)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, privateInput io.Reader, out io.Writer) error {
	var c candidate.Config
	var keyFile, published string
	f := flag.NewFlagSet("package", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&c.SourceRoot, "source", ".", "repository root containing deploy and notices")
	f.StringVar(&c.Output, "out", "", "new output directory (must not exist)")
	f.StringVar(&c.Version, "version", "", "stable-format candidate tag vMAJOR.MINOR.PATCH")
	f.StringVar(&c.Repository, "repository", "", "owner/repository identity")
	f.Int64Var(&c.Schema, "schema", 0, "unchanged manager database schema")
	f.StringVar(&published, "published-at", "", "fixed whole-second UTC RFC3339 time")
	f.StringVar(&c.LinuxManager, "linux-manager", "", "stamped native Linux manager with .version.json receipt")
	f.StringVar(&c.WindowsManager, "windows-manager", "", "stamped native Windows manager with .version.json receipt")
	f.StringVar(&c.LinuxUpdater, "linux-updater", "", "stamped native Linux updater with .version.json receipt")
	f.StringVar(&c.WindowsUpdater, "windows-updater", "", "stamped native Windows updater with .version.json receipt")
	f.StringVar(&c.KeyID, "key-id", "", "supplied signing key identity")
	f.StringVar(&keyFile, "signing-key-file", "", "private raw/base64 Ed25519 key file; '-' reads private stdin")
	f.BoolVar(&c.Development, "development", false, "generate in-memory ephemeral development trust only")
	if e := f.Parse(args); errors.Is(e, flag.ErrHelp) {
		_, e = fmt.Fprintln(out, "Usage: go run ./release/cmd/package [flags]\nPrivate signing material is accepted only by --signing-key-file <private file|->.\nUse --development for disposable fixture trust; see release/CANDIDATES.md.")
		return e
	} else if e != nil || len(f.Args()) != 0 {
		return errors.New("invalid package flags (see release/CANDIDATES.md)")
	}
	if c.Development == (keyFile != "") {
		return errors.New("choose exactly one signing mode: --development or --signing-key-file")
	}
	var e error
	c.PublishedAt, e = time.Parse(time.RFC3339, published)
	if e != nil {
		return errors.New("--published-at requires a fixed whole-second UTC RFC3339 time")
	}
	if c.Development {
		if c.KeyID == "" {
			c.KeyID = "development-ephemeral"
		}
		_, c.SigningKey, e = ed25519.GenerateKey(rand.Reader)
	} else if keyFile == "-" {
		c.SigningKey, e = candidate.ReadSigningKey(privateInput)
	} else {
		c.SigningKey, e = candidate.OpenSigningKey(keyFile)
	}
	if e != nil {
		return e
	}
	defer func() {
		for i := range c.SigningKey {
			c.SigningKey[i] = 0
		}
	}()
	if e = candidate.Produce(ctx, c); e != nil {
		return e
	}
	label := "OWNER SIGNED; qualification and publication require separate approval"
	if c.Development {
		label = "DEVELOPMENT ONLY; public fixture trust is not production trust"
	}
	_, e = fmt.Fprintln(out, "Candidate produced:", c.Output, "("+label+")")
	return e
}
