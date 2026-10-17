// qualify-installer produces disposable signed candidates for native Linux
// installer lifecycle qualification. Its signing key exists only in memory.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

const fixtureKeyID = "linux-installer-fixture"

type options struct{ source, input, output string }
type receipt struct {
	Format       int    `json:"format"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Command      string `json:"command"`
	Version      string `json:"version"`
	BinarySHA256 string `json:"binary_sha256"`
	Schema       int64  `json:"schema,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "installer qualification fixture production refused")
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if ctx == nil || out == nil || runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return errors.New("native Linux amd64 fixture producer required")
	}
	var o options
	f := flag.NewFlagSet("qualify-installer", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.source, "source-root", "", "repository root containing deploy assets")
	f.StringVar(&o.input, "input-root", "", "native CI manager/updater/bootstrap inputs")
	f.StringVar(&o.output, "output", "", "new candidate output directory")
	if err := f.Parse(args); err != nil || f.NArg() != 0 || o.source == "" || o.input == "" || o.output == "" {
		return errors.New("invalid fixture producer arguments")
	}
	return produce(ctx, o, out)
}

func produce(ctx context.Context, o options, out io.Writer) (err error) {
	if ctx == nil || ctx.Err() != nil || o.source == "" || o.input == "" || o.output == "" || !filepath.IsAbs(o.source) || !filepath.IsAbs(o.input) || !filepath.IsAbs(o.output) {
		return errors.New("absolute fixture paths and live context required")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return errors.New("native Linux amd64 fixture producer required")
	}
	if _, err = os.Lstat(o.output); !errors.Is(err, os.ErrNotExist) {
		return errors.New("fixture output already exists or cannot be inspected")
	}
	parent := filepath.Dir(o.output)
	if info, e := os.Stat(parent); e != nil || !info.IsDir() {
		return errors.New("fixture output parent must exist")
	}
	if err = validateNativeInputs(o.input); err != nil {
		return err
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	defer clear(key)
	if err = os.Mkdir(o.output, 0700); err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			err = errors.Join(err, os.RemoveAll(o.output))
		}
	}()
	managerSchema, e := nativeReceiptSchema(filepath.Join(o.input, "linux", "v0.0.1", "manager"), "linux", "minecraft-ui", "v0.0.1")
	if e != nil {
		return e
	}
	for _, version := range []string{"v0.0.1", "v0.0.2", "v0.0.3"} {
		if err = ctx.Err(); err != nil {
			return err
		}
		linuxManager := inputPath(o.input, "linux", version, "manager")
		windowsManager := inputPath(o.input, "windows", version, "manager")
		linuxUpdater := inputPath(o.input, "linux", version, "updater")
		windowsUpdater := inputPath(o.input, "windows", version, "updater")
		config := candidate.Config{
			SourceRoot: o.source, Output: filepath.Join(o.output, version), Version: version,
			Repository: "jonasthim/minecraft-server-ui", Schema: managerSchema,
			PublishedAt: time.Now().UTC().Truncate(time.Second), KeyID: fixtureKeyID,
			SigningKey: key, Development: true,
			LinuxManager: linuxManager, WindowsManager: windowsManager,
			LinuxUpdater: linuxUpdater, WindowsUpdater: windowsUpdater,
		}
		if err = candidate.Produce(ctx, config); err != nil {
			return fmt.Errorf("produce disposable candidate %s: %w", version, err)
		}
	}
	if err = verifySharedPublicTrust(o.output); err != nil {
		return err
	}
	summary := struct {
		Format       int      `json:"format"`
		Scope        string   `json:"scope"`
		Trust        string   `json:"trust"`
		Versions     []string `json:"versions"`
		FailureImage string   `json:"failure_image"`
	}{1, "native-linux-installer-fixture-candidates", "ephemeral-development-public-key; no private key written", []string{"v0.0.1", "v0.0.2", "v0.0.3"}, "v0.0.3 Linux manager is an intentionally unhealthy overlay fixture"}
	body, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	if err = writeExclusive(filepath.Join(o.output, "qualification-fixtures.json"), append(body, '\n'), 0644); err != nil {
		return err
	}
	published = true
	_, err = fmt.Fprintln(out, "Produced three authenticated disposable development candidates; no private key was retained.")
	return err
}

func inputPath(root, platform, version, name string) string {
	if platform == "windows" {
		name += ".exe"
	}
	return filepath.Join(root, platform, version, name)
}

func validateNativeInputs(root string) error {
	if !filepath.IsAbs(root) {
		return errors.New("native input root must be absolute")
	}
	for _, platform := range []string{"linux", "windows"} {
		for _, version := range []string{"v0.0.1", "v0.0.2", "v0.0.3"} {
			for _, item := range []struct{ name, command string }{
				{"manager", "minecraft-ui"}, {"updater", "minecraft-ui-update"}, {"bootstrap", "minecraft-ui-bootstrap"},
			} {
				if _, err := nativeReceiptSchema(inputPath(root, platform, version, item.name), platform, item.command, version); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func nativeReceiptSchema(binaryPath, platform, command, version string) (int64, error) {
	info, err := os.Lstat(binaryPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 1 {
		return 0, errors.New("native qualification binary is absent or unsafe")
	}
	receiptPath := binaryPath + ".version.json"
	receiptInfo, err := os.Lstat(receiptPath)
	if err != nil || !receiptInfo.Mode().IsRegular() || receiptInfo.Mode()&os.ModeSymlink != 0 || receiptInfo.Size() < 1 || receiptInfo.Size() > 4096 {
		return 0, errors.New("native qualification execution receipt is absent or unsafe")
	}
	body, err := os.ReadFile(receiptPath)
	if err != nil {
		return 0, errors.New("native qualification receipt could not be read")
	}
	var r receipt
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return 0, errors.New("native qualification receipt is invalid")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return 0, errors.New("native qualification receipt has trailing data")
	}
	if r.Format != 1 || r.OS != platform || r.Arch != "amd64" || r.Command != command || r.Version != version {
		return 0, errors.New("native qualification receipt identity mismatch")
	}
	if command == "minecraft-ui" && r.Schema < 1 || command != "minecraft-ui" && r.Schema != 0 {
		return 0, errors.New("native qualification receipt schema mismatch")
	}
	if !validHexSHA256(r.BinarySHA256) {
		return 0, errors.New("native qualification receipt digest invalid")
	}
	// The candidate package verifies this receipt against binary bytes before
	// packaging manager/updater members. Bootstrap receipt binding is checked here.
	if command == "minecraft-ui-bootstrap" {
		actual, err := fileSHA256(binaryPath)
		if err != nil || actual != r.BinarySHA256 {
			return 0, errors.New("bootstrap execution receipt does not bind binary bytes")
		}
	}
	return r.Schema, nil
}

func verifySharedPublicTrust(root string) error {
	var expected []byte
	for _, version := range []string{"v0.0.1", "v0.0.2", "v0.0.3"} {
		body, err := os.ReadFile(filepath.Join(root, version, "development-trust.json"))
		if err != nil {
			return errors.New("candidate public trust output is incomplete")
		}
		if expected == nil {
			expected = body
		} else if string(expected) != string(body) {
			return errors.New("fixture candidates do not share one ephemeral public trust")
		}
	}
	return nil
}

func writeExclusive(path string, body []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(body)
	return errors.Join(writeErr, f.Sync(), f.Close())
}

func validHexSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, f)
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
