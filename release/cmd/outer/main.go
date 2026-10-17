// Command outer creates prepare-only entry scripts from independently reviewed
// public trust and exact native preparer inputs. It never executes input files.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/jonasthim/minecraft-server-ui/release/outer"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "outer entry generation refused:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	var c outer.Config
	f := flag.NewFlagSet("outer", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&c.Repository, "repository", "", "reviewed owner/repository")
	f.StringVar(&c.Version, "version", "", "exact preparer release vMAJOR.MINOR.PATCH")
	f.StringVar(&c.LinuxPreparer, "linux-preparer", "", "Linux preparer with adjacent .version.json receipt")
	f.StringVar(&c.WindowsPreparer, "windows-preparer", "", "Windows preparer with adjacent .version.json receipt")
	f.StringVar(&c.TrustFile, "trust-file", "", "independently reviewed public trust file")
	f.StringVar(&c.Output, "out", "", "new absolute output directory in a private trusted parent")
	f.BoolVar(&c.Development, "development", false, "visibly development-only entry scripts")
	if err := f.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(out, "Usage: go run ./release/cmd/outer --repository OWNER/REPO --version vX.Y.Z --linux-preparer FILE --windows-preparer FILE --trust-file FILE --out ABSNEWPATH [--development]")
		return err
	} else if err != nil || len(f.Args()) != 0 {
		return errors.New("invalid outer generation flags")
	}
	if err := outer.Produce(ctx, c); err != nil {
		return err
	}
	_, err := fmt.Fprintln(out, "Prepare-only entry artifacts produced; initial trust review, native execution and publication are separate gates.")
	return err
}
