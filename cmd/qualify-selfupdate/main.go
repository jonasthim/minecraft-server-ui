// qualify-selfupdate is an offline acquisition fixture for disposable native
// acceptance runners. It is never installed as a product or release member.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
)

const fixtureRepository = "qualification/installed-updater"
const fixtureKeyID = "disposable-task32b"

func fixtureSchema() int64 { return db.LatestVersion() }

type prepareConfig struct{ Success, Negative, PeerSuccess, PeerNegative, License, Notices, Output string }

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		// Input paths, arbitrary file content and error chains are never diagnostic artifacts.
		fmt.Fprintln(os.Stderr, "FAIL updater acquisition fixture")
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New("command required")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	f.SetOutput(io.Discard)
	switch args[0] {
	case "prepare":
		var c prepareConfig
		f.StringVar(&c.Success, "success", "", "")
		f.StringVar(&c.Negative, "negative", "", "")
		f.StringVar(&c.PeerSuccess, "peer-success", "", "")
		f.StringVar(&c.PeerNegative, "peer-negative", "", "")
		f.StringVar(&c.License, "license", "", "")
		f.StringVar(&c.Notices, "notices", "", "")
		f.StringVar(&c.Output, "out", "", "")
		if err := f.Parse(args[1:]); err != nil || f.NArg() != 0 {
			return errors.New("invalid preparation input")
		}
		return prepare(ctx, c)
	case "stage":
		bundle := f.String("bundle", "", "")
		version := f.String("version", "", "")
		current := f.String("current", "", "")
		dir := f.String("stage-dir", "", "")
		if err := f.Parse(args[1:]); err != nil || f.NArg() != 0 {
			return errors.New("invalid staging input")
		}
		if *version != "v0.0.2" && *version != "v0.0.3" {
			return errors.New("fixture version required")
		}
		trust, err := selfupdate.LoadNativeTrust(ctx)
		if err != nil {
			return err
		}
		root := filepath.Join(*bundle, *version)
		body, err := readRegular(filepath.Join(root, "release.json"), selfupdate.MaxManifestBytes)
		if err != nil {
			return err
		}
		sig, err := readRegular(filepath.Join(root, "release.sig"), 1024)
		if err != nil {
			return err
		}
		archiveName := "minecraft-ui-linux-amd64.tar.gz"
		if runtime.GOOS == "windows" {
			archiveName = "minecraft-ui-windows-amd64.zip"
		}
		archive, err := os.Open(filepath.Join(root, archiveName))
		if err != nil {
			return err
		}
		defer archive.Close()
		stager := selfupdate.Stager{Dir: *dir, Trust: trust, CurrentVersion: *current, Schema: fixtureSchema(), OS: runtime.GOOS, Arch: runtime.GOARCH}
		stage, err := stager.Stage(ctx, *version, body, sig, archive)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(stage)
	default:
		return errors.New("unknown command")
	}
}
func readRegular(path string, limit int64) ([]byte, error) {
	if path == "" {
		return nil, errors.New("path required")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > limit {
		return nil, errors.New("unsafe fixture input")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("fixture binding changed")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || int64(len(b)) != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, errors.New("fixture changed")
	}
	return b, nil
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func makeArchive(platform string, binary, license, notices []byte) ([]byte, selfupdate.Artifact, error) {
	var buf bytes.Buffer
	name := "minecraft-ui"
	archiveName := "minecraft-ui-linux-amd64.tar.gz"
	if platform == "windows" {
		name += ".exe"
		archiveName = "minecraft-ui-windows-amd64.zip"
	}
	members := []struct {
		name string
		body []byte
	}{{name, binary}, {"LICENSE", license}, {"THIRD_PARTY_NOTICES.txt", notices}}
	if platform == "windows" {
		z := zip.NewWriter(&buf)
		for _, m := range members {
			h := &zip.FileHeader{Name: m.name, Method: zip.Store}
			h.SetMode(0644)
			w, err := z.CreateHeader(h)
			if err != nil {
				return nil, selfupdate.Artifact{}, err
			}
			if _, err = w.Write(m.body); err != nil {
				return nil, selfupdate.Artifact{}, err
			}
		}
		if err := z.Close(); err != nil {
			return nil, selfupdate.Artifact{}, err
		}
	} else {
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for _, m := range members {
			h := &tar.Header{Name: m.name, Mode: 0644, Size: int64(len(m.body)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
			if err := tw.WriteHeader(h); err != nil {
				return nil, selfupdate.Artifact{}, err
			}
			if _, err := tw.Write(m.body); err != nil {
				return nil, selfupdate.Artifact{}, err
			}
		}
		if err := tw.Close(); err != nil {
			return nil, selfupdate.Artifact{}, err
		}
		if err := gz.Close(); err != nil {
			return nil, selfupdate.Artifact{}, err
		}
	}
	b := buf.Bytes()
	return b, selfupdate.Artifact{OS: platform, Arch: "amd64", Name: archiveName, Size: int64(len(b)), SHA256: digest(b), BinarySize: int64(len(binary)), BinarySHA256: digest(binary)}, nil
}
func signManifest(key ed25519.PrivateKey, version string, artifacts []selfupdate.Artifact, now time.Time) ([]byte, []byte, error) {
	m := selfupdate.Manifest{Format: 1, Repository: fixtureRepository, Version: version, PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(24 * time.Hour), SchemaBefore: fixtureSchema(), SchemaAfter: fixtureSchema(), Artifacts: artifacts}
	body, err := json.Marshal(m)
	if err != nil {
		return nil, nil, err
	}
	sig, err := json.Marshal(selfupdate.Signature{KeyID: fixtureKeyID, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, body))})
	return body, sig, err
}
func prepare(ctx context.Context, c prepareConfig) error {
	if c.Output == "" || (runtime.GOOS != "linux" && runtime.GOOS != "windows") || runtime.GOARCH != "amd64" {
		return errors.New("native amd64 fixture required")
	}
	license, err := readRegular(c.License, 1<<20)
	if err != nil {
		return err
	}
	notices, err := readRegular(c.Notices, 1<<20)
	if err != nil {
		return err
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	defer clear(key)
	if err := os.Mkdir(c.Output, 0700); err != nil {
		return err
	}
	trust := struct {
		Format     int               `json:"format"`
		Repository string            `json:"repository"`
		PublicKeys map[string]string `json:"public_keys"`
	}{1, fixtureRepository, map[string]string{fixtureKeyID: base64.StdEncoding.EncodeToString(pub)}}
	body, err := json.Marshal(trust)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(c.Output, "public-trust.json"), body, 0600); err != nil {
		return err
	}
	peer := "windows"
	if runtime.GOOS == "windows" {
		peer = "linux"
	}
	for _, release := range []struct{ version, native, other string }{{"v0.0.2", c.Success, c.PeerSuccess}, {"v0.0.3", c.Negative, c.PeerNegative}} {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir := filepath.Join(c.Output, release.version)
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		var artifacts []selfupdate.Artifact
		for _, input := range []struct{ platform, path string }{{runtime.GOOS, release.native}, {peer, release.other}} {
			binary, err := readRegular(input.path, selfupdate.MaxArtifactBytes)
			if err != nil {
				return err
			}
			archive, a, err := makeArchive(input.platform, binary, license, notices)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, a.Name), archive, 0600); err != nil {
				return err
			}
			artifacts = append(artifacts, a)
		}
		metadata, sig, err := signManifest(key, release.version, artifacts, time.Now().UTC())
		if err != nil {
			return err
		}
		// Verify before making the bundle available; staging/native import reverify independently.
		t := selfupdate.Trust{Repository: fixtureRepository, PublicKeys: map[string]ed25519.PublicKey{fixtureKeyID: pub}}
		if _, err := selfupdate.VerifyManifest(t, metadata, sig, release.version, "v0.0.1", fixtureSchema(), time.Now()); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "release.json"), metadata, 0600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "release.sig"), sig, 0600); err != nil {
			return err
		}
	}
	return nil
}
