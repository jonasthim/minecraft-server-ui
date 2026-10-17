// Command verify checks local candidate files with the shipping update verifier
// and bootstrap contract. It never executes the supplied manager/updater.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if e := run(ctx, os.Args[1:], os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, "candidate verification refused:", e)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out io.Writer) (err error) {
	var artifacts, trustFile, repository, version, at string
	var schema int64
	f := flag.NewFlagSet("verify", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&artifacts, "artifacts", "", "local candidate directory")
	f.StringVar(&trustFile, "trust-file", "", "independently supplied public trust file")
	f.StringVar(&repository, "repository", "", "expected owner/repository")
	f.StringVar(&version, "version", "", "expected exact version")
	f.Int64Var(&schema, "schema", 0, "expected unchanged schema")
	f.StringVar(&at, "now", "", "fixture-only explicit UTC verification time")
	if e := f.Parse(args); errors.Is(e, flag.ErrHelp) {
		_, e = fmt.Fprintln(out, "Usage: go run ./release/cmd/verify --artifacts DIR --trust-file FILE --repository OWNER/REPO --version vMAJOR.MINOR.PATCH --schema N\nChecks artifacts only; downloaded fixture trust does not establish production publisher identity.")
		return e
	} else if e != nil || len(f.Args()) != 0 {
		return errors.New("invalid verification flags")
	}
	if artifacts == "" || trustFile == "" || repository == "" || version == "" || schema < 1 {
		return errors.New("explicit artifacts, public trust, repository, version and positive schema are required")
	}
	now := time.Now().UTC()
	if at != "" {
		var e error
		now, e = time.Parse(time.RFC3339, at)
		if e != nil {
			return errors.New("invalid verification time")
		}
	}
	trustBody, e := read(ctx, trustFile, 16<<10)
	if e != nil {
		return e
	}
	trust, e := candidate.ParsePublicTrust(trustBody, repository)
	if e != nil {
		return e
	}
	shippingTrust := selfupdate.Trust(trust)
	body, e := read(ctx, filepath.Join(artifacts, "release.json"), selfupdate.MaxManifestBytes)
	if e != nil {
		return e
	}
	sig, e := read(ctx, filepath.Join(artifacts, "release.sig"), 1024)
	if e != nil {
		return e
	}
	m, e := selfupdate.VerifyManifest(shippingTrust, body, sig, version, "0.0.0-dev", schema, now)
	if e != nil {
		return e
	}
	bootstrapBody, e := read(ctx, filepath.Join(artifacts, "bootstrap.json"), candidate.MaxBootstrapBytes)
	if e != nil {
		return e
	}
	bootstrapSig, e := read(ctx, filepath.Join(artifacts, "bootstrap.sig"), 1024)
	if e != nil {
		return e
	}
	bootstrap, e := candidate.VerifyBootstrap(trust, bootstrapBody, bootstrapSig, version, now)
	if e != nil {
		return e
	}
	if bootstrap.Schema != schema || !bootstrap.PublishedAt.Equal(m.PublishedAt) || !bootstrap.ExpiresAt.Equal(m.ExpiresAt) {
		return errors.New("bootstrap/update schema or validity identity differs")
	}
	temp, e := os.MkdirTemp("", "minecraft-ui-candidate-check-")
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, os.RemoveAll(temp)) }()
	for _, bundle := range bootstrap.Artifacts {
		artifact, e := m.Artifact(bundle.OS, bundle.Arch)
		if e != nil {
			return e
		}
		if bundle.Members[0].Size != artifact.BinarySize || bundle.Members[0].SHA256 != artifact.BinarySHA256 {
			return errors.New("bootstrap/update manager bytes differ")
		}
		dir := filepath.Join(temp, bundle.OS)
		if e = os.Mkdir(dir, 0700); e != nil {
			return e
		}
		archive, e := open(ctx, filepath.Join(artifacts, artifact.Name))
		if e != nil {
			return e
		}
		stager := selfupdate.Stager{Dir: dir, Trust: shippingTrust, CurrentVersion: "0.0.0-dev", Schema: schema, OS: bundle.OS, Arch: bundle.Arch, Now: func() time.Time { return now }}
		stage, stageErr := stager.Stage(ctx, version, body, sig, archive)
		e = errors.Join(stageErr, archive.Close())
		if e != nil {
			return fmt.Errorf("shipping %s staging: %w", bundle.OS, e)
		}
		for _, member := range bundle.Members[2:4] {
			notice, e := read(ctx, filepath.Join(dir, stage.ID, member.Name), candidate.MaxAssetBytes)
			if e != nil {
				return e
			}
			sum := sha256.Sum256(notice)
			if int64(len(notice)) != member.Size || hex.EncodeToString(sum[:]) != member.SHA256 {
				return errors.New("bootstrap/update notice bytes differ")
			}
		}
		archive, e = open(ctx, filepath.Join(artifacts, bundle.Name))
		if e != nil {
			return e
		}
		e = errors.Join(candidate.VerifyBundle(ctx, bundle, archive), archive.Close())
		if e != nil {
			return fmt.Errorf("bootstrap %s verification: %w", bundle.OS, e)
		}
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	label := "OWNER SIGNED; qualification is separate"
	if bootstrap.Trust == "DEVELOPMENT ONLY" {
		label = "DEVELOPMENT ONLY; fixture consistency, not production publisher identity"
	}
	_, e = fmt.Fprintln(out, "Verified", version, "both shipping update stages and bootstrap bundles ("+label+")")
	return e
}
func open(ctx context.Context, path string) (*os.File, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	absolute, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	for p := absolute; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil {
			return nil, e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("candidate input or parent is a symlink")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	before, e := os.Lstat(absolute)
	if e != nil {
		return nil, e
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("candidate input must be an ordinary file")
	}
	f, e := os.Open(absolute)
	if e != nil {
		return nil, e
	}
	after, e := f.Stat()
	if e != nil || !os.SameFile(before, after) {
		_ = f.Close()
		return nil, errors.New("candidate input binding changed")
	}
	return f, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func read(ctx context.Context, path string, max int64) (body []byte, err error) {
	f, err := open(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	body, err = io.ReadAll(io.LimitReader(contextReader{ctx, f}, max+1))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || int64(len(body)) > max {
		return nil, errors.New("candidate input exceeds bounds")
	}
	return body, nil
}
