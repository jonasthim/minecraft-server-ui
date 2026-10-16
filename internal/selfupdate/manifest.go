// Package selfupdate verifies and stages manager releases. It never modifies game data.
package selfupdate

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const MaxManifestBytes = 32 << 10
const MaxArtifactBytes int64 = 256 << 20

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
var versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Trust must come from reviewed native configuration, never release metadata.
// The empty value deliberately disables verification and downloads.
type Trust struct {
	Repository string
	PublicKeys map[string]ed25519.PublicKey
}

func (t Trust) Validate() error {
	if !repositoryPattern.MatchString(t.Repository) || strings.Contains(t.Repository, "..") || len(t.PublicKeys) == 0 || len(t.PublicKeys) > 16 {
		return errors.New("release source and verification keys are not configured")
	}
	for id, key := range t.PublicKeys {
		if !keyPattern.MatchString(id) || len(key) != ed25519.PublicKeySize {
			return errors.New("invalid release verification key configuration")
		}
	}
	return nil
}

type Signature struct {
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}
type Artifact struct {
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	BinarySize   int64  `json:"binary_size"`
	BinarySHA256 string `json:"binary_sha256"`
}
type Manifest struct {
	Format       int        `json:"format"`
	Repository   string     `json:"repository"`
	Version      string     `json:"version"`
	PublishedAt  time.Time  `json:"published_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	SchemaBefore int64      `json:"schema_before"`
	SchemaAfter  int64      `json:"schema_after"`
	Artifacts    []Artifact `json:"artifacts"`
}

func ParseStable(value string) ([3]uint64, error) {
	var out [3]uint64
	parts := versionPattern.FindStringSubmatch(value)
	if parts == nil {
		return out, errors.New("release version must be a stable vMAJOR.MINOR.PATCH")
	}
	for i := range out {
		n, err := strconv.ParseUint(parts[i+1], 10, 64)
		if err != nil {
			return out, errors.New("release version component overflows")
		}
		out[i] = n
	}
	return out, nil
}
func newer(candidate, current [3]uint64) bool {
	for i := range candidate {
		if candidate[i] != current[i] {
			return candidate[i] > current[i]
		}
	}
	return false
}
func validHash(value string) bool {
	b, e := hex.DecodeString(value)
	return e == nil && len(b) == 32 && value == strings.ToLower(value)
}
func archiveName(platform string) string {
	if platform == "linux" {
		return "minecraft-ui-linux-amd64.tar.gz"
	}
	return "minecraft-ui-windows-amd64.zip"
}
func binaryName(platform string) string {
	if platform == "linux" {
		return "minecraft-ui"
	}
	return "minecraft-ui.exe"
}

// VerifyManifest checks signature before interpreting metadata. The unchanged
// schema rule is intentional: schema-changing upgrades need manual maintenance.
func VerifyManifest(trust Trust, body, envelope []byte, tag, current string, schema int64, now time.Time) (Manifest, error) {
	var m Manifest
	if err := trust.Validate(); err != nil {
		return m, err
	}
	if len(body) == 0 || len(body) > MaxManifestBytes || len(envelope) > 1024 {
		return m, errors.New("release metadata exceeds bounds")
	}
	var sig Signature
	if err := strictJSON(envelope, &sig); err != nil {
		return m, errors.New("invalid release signature envelope")
	}
	key, ok := trust.PublicKeys[sig.KeyID]
	if !ok {
		return m, errors.New("release signing key is not trusted")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(sig.Signature)
	if err != nil || !ed25519.Verify(key, body, signature) {
		return m, errors.New("release signature verification failed")
	}
	if err := strictJSON(body, &m); err != nil {
		return m, errors.New("invalid signed release metadata")
	}
	candidate, err := ParseStable(m.Version)
	if err != nil {
		return m, err
	}
	if current != "0.0.0-dev" {
		old, e := ParseStable(current)
		if e != nil || !newer(candidate, old) {
			return m, errors.New("release is not newer than the running stable manager")
		}
	}
	if m.Format != 1 || m.Repository != trust.Repository || m.Version != tag {
		return m, errors.New("signed release identity does not match configured source or requested version")
	}
	if m.PublishedAt.IsZero() || m.PublishedAt.After(now) || !m.ExpiresAt.After(now) || !m.ExpiresAt.After(m.PublishedAt) || m.ExpiresAt.Sub(m.PublishedAt) > 30*24*time.Hour {
		return m, errors.New("release metadata validity window is not current")
	}
	if schema < 1 || m.SchemaBefore != schema || m.SchemaAfter != schema {
		return m, errors.New("schema-changing manager updates require manual maintenance")
	}
	if len(m.Artifacts) != 2 {
		return m, errors.New("release must contain exactly the two qualified target platforms")
	}
	seen := map[string]bool{}
	for _, a := range m.Artifacts {
		if (a.OS != "linux" && a.OS != "windows") || a.Arch != "amd64" || seen[a.OS] || a.Name != archiveName(a.OS) || a.Size < 1 || a.Size > MaxArtifactBytes || a.BinarySize < 1 || a.BinarySize > MaxArtifactBytes || !validHash(a.SHA256) || !validHash(a.BinarySHA256) {
			return m, errors.New("invalid signed release artifact")
		}
		seen[a.OS] = true
	}
	return m, nil
}
func (m Manifest) Artifact(platform, arch string) (Artifact, error) {
	for _, a := range m.Artifacts {
		if a.OS == platform && a.Arch == arch {
			return a, nil
		}
	}
	return Artifact{}, errors.New("manager update platform is unavailable")
}

// strictJSON also rejects duplicate object members so independent verifiers
// cannot interpret the same signed bytes differently.
func strictJSON(body []byte, dst any) error {
	scan := json.NewDecoder(bytes.NewReader(body))
	if err := scanValue(scan, 0); err != nil {
		return err
	}
	if _, err := scan.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}
func scanValue(d *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("JSON nesting limit")
	}
	tok, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return e
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate JSON member")
			}
			seen[name] = true
			if err := scanValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := scanValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("invalid JSON delimiter")
	}
	_, err = d.Token()
	return err
}
