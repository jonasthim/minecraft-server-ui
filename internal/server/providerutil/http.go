// Package providerutil contains the bounded official-provider HTTP and immutable
// JAR publication policy shared by Vanilla and Paper. It never starts a server.
package providerutil

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type Publisher string

const (
	Mojang           Publisher = "mojang"
	Paper            Publisher = "paper"
	MaxMetadataBytes int64     = 8 << 20
	MaxArtifactBytes int64     = 512 << 20
	userAgent                  = "minecraft-server-ui/dev (https://github.com/jonasthim/minecraft-server-ui)"
)

var (
	hex40          = regexp.MustCompile(`^[a-f0-9]{40}$`)
	hex64          = regexp.MustCompile(`^[a-f0-9]{64}$`)
	mojangMetadata = regexp.MustCompile(`^/v1/packages/[a-f0-9]{40}/[0-9]+\.[0-9]+(?:\.[0-9]+)?\.json$`)
	mojangArtifact = regexp.MustCompile(`^/v1/objects/[a-f0-9]{40}/server\.jar$`)
	paperMetadata  = regexp.MustCompile(`^/v3/projects/paper(?:/versions/[0-9]+\.[0-9]+(?:\.[0-9]+)?/builds)?$`)
	paperArtifact  = regexp.MustCompile(`^/v1/objects/[a-f0-9]{64}/paper-[0-9]+\.[0-9]+(?:\.[0-9]+)?-[1-9][0-9]*\.jar$`)
)

// ValidateURL accepts only the exact official endpoints needed by this package.
// Redirects are disabled: an official direct URL must not silently cross trust boundaries.
func ValidateURL(publisher Publisher, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Host != u.Hostname() {
		return errors.New("invalid publisher URL")
	}
	allowed := false
	switch publisher {
	case Mojang:
		allowed = (u.Host == "piston-meta.mojang.com" && (u.Path == "/mc/game/version_manifest_v2.json" || mojangMetadata.MatchString(u.Path))) || ((u.Host == "piston-data.mojang.com" || u.Host == "launcher.mojang.com") && mojangArtifact.MatchString(u.Path))
	case Paper:
		allowed = (u.Host == "fill.papermc.io" && paperMetadata.MatchString(u.Path)) || (u.Host == "fill-data.papermc.io" && paperArtifact.MatchString(u.Path))
	}
	if !allowed {
		return errors.New("URL is outside official provider endpoints")
	}
	return nil
}

func ValidDigest(algorithm, value string) bool {
	return algorithm == "sha1" && hex40.MatchString(value) || algorithm == "sha256" && hex64.MatchString(value)
}

// CheckArtifact also binds the digest and filename to the official object URL.
func CheckArtifact(publisher Publisher, a domain.Artifact) error {
	if err := ValidateURL(publisher, a.SourceURL); err != nil {
		return err
	}
	if a.Provider != string(publisher) || a.Size <= 0 || a.Size > MaxArtifactBytes || domain.ValidateFilename(a.Filename) != nil || !strings.HasSuffix(a.Filename, ".jar") {
		return errors.New("invalid publisher artifact")
	}
	u, _ := url.Parse(a.SourceURL)
	switch publisher {
	case Mojang:
		if a.Checksum.Algorithm != "sha1" || !hex40.MatchString(a.Checksum.Value) || !mojangArtifact.MatchString(u.Path) || !strings.Contains(u.Path, "/"+a.Checksum.Value+"/") {
			return errors.New("invalid Mojang artifact digest or URL")
		}
	case Paper:
		if a.Checksum.Algorithm != "sha256" || !hex64.MatchString(a.Checksum.Value) || !paperArtifact.MatchString(u.Path) || !strings.HasSuffix(u.Path, "/"+a.Checksum.Value+"/"+a.Filename) {
			return errors.New("invalid Paper artifact digest or URL")
		}
	default:
		return errors.New("unknown publisher")
	}
	return nil
}

type Source struct {
	URL               string           `json:"url"`
	SHA256            string           `json:"sha256"`
	RetrievedAt       time.Time        `json:"retrieved_at"`
	PublishedChecksum *domain.Checksum `json:"published_checksum,omitempty"`
}
type Client struct {
	http      *http.Client
	publisher Publisher
}

func NewClient(client *http.Client, publisher Publisher) *Client {
	if client == nil {
		client = &http.Client{}
	}
	clone := *client
	if clone.Timeout <= 0 || clone.Timeout > 2*time.Minute {
		clone.Timeout = 2 * time.Minute
	}
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("publisher redirects are disabled") }
	// No cookie jar or credentials are required by official public catalogs.
	clone.Jar = nil
	return &Client{http: &clone, publisher: publisher}
}
func (c *Client) request(ctx context.Context, raw string) (*http.Response, error) {
	if err := ValidateURL(c.publisher, raw); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, errors.New("create publisher request")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, application/java-archive, application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("publisher request: %w", ctx.Err())
		}
		return nil, errors.New("publisher request failed")
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("publisher returned HTTP %d", resp.StatusCode)
	}
	return resp, nil
}
func (c *Client) MetadataBytes(ctx context.Context, raw string) ([]byte, Source, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.request(ctx, raw)
	if err != nil {
		return nil, Source{}, err
	}
	defer resp.Body.Close()
	if resp.ContentLength > MaxMetadataBytes {
		return nil, Source{}, errors.New("publisher metadata is too large")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxMetadataBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, Source{}, ctx.Err()
		}
		return nil, Source{}, errors.New("read publisher metadata")
	}
	if int64(len(data)) > MaxMetadataBytes {
		return nil, Source{}, errors.New("publisher metadata is too large")
	}
	sum := sha256.Sum256(data)
	return data, Source{URL: raw, SHA256: hex.EncodeToString(sum[:]), RetrievedAt: time.Now().UTC()}, nil
}
func (c *Client) Metadata(ctx context.Context, raw string, dest any) (Source, error) {
	b, s, err := c.MetadataBytes(ctx, raw)
	if err != nil {
		return Source{}, err
	}
	if err = json.Unmarshal(b, dest); err != nil {
		return Source{}, errors.New("invalid publisher metadata JSON")
	}
	return s, nil
}
func (c *Client) download(ctx context.Context, a domain.Artifact, w io.Writer) error {
	if err := CheckArtifact(c.publisher, a); err != nil {
		return err
	}
	resp, err := c.request(ctx, a.SourceURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.ContentLength > 0 && resp.ContentLength != a.Size {
		return errors.New("artifact Content-Length differs from publisher size")
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, a.Size+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("artifact download interrupted")
	}
	if n != a.Size {
		return errors.New("artifact size differs from publisher metadata")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return nil
}

// SamePlan compares the fields which affect executable installation. Catalog
// fetch times, capability descriptions and warnings are deliberately excluded.
func SamePlan(a, b domain.InstallPlan) bool { return bytes.Equal(planIdentity(a), planIdentity(b)) }
func planIdentity(p domain.InstallPlan) []byte {
	artifacts := append([]domain.Artifact(nil), p.Artifacts...)
	for n := range artifacts {
		artifacts[n].RetrievedAt = time.Time{}
	}
	value := struct {
		Selection    domain.VersionSelection
		Requirements domain.LaunchRequirements
		Artifacts    []domain.Artifact
	}{p.Selection, p.Requirements, artifacts}
	b, _ := json.Marshal(value)
	return b
}
func PlanID(p domain.InstallPlan) string {
	sum := sha256.Sum256(planIdentity(p))
	return hex.EncodeToString(sum[:])
}

// Download streams an exact official artifact and verifies its published digest.
// It returns an independently computed local SHA256. On error the writer may
// contain partial bytes; callers must discard their staging destination.
func (c *Client) Download(ctx context.Context, a domain.Artifact, w io.Writer) (string, error) {
	var published hash.Hash = sha256.New()
	if a.Checksum.Algorithm == "sha1" {
		published = sha1.New()
	}
	local := sha256.New()
	if err := c.download(ctx, a, io.MultiWriter(w, published, local)); err != nil {
		return "", err
	}
	if hex.EncodeToString(published.Sum(nil)) != a.Checksum.Value {
		return "", errors.New("artifact checksum verification failed")
	}
	return hex.EncodeToString(local.Sum(nil)), nil
}
