// Package loaderutil contains bounded publisher I/O and immutable multi-file loader publication.
package loaderutil

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
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

const UserAgent = "minecraft-server-ui/dev (https://github.com/jonasthim/minecraft-server-ui)"
const MaxArtifact int64 = 512 << 20

var token = regexp.MustCompile(`^[A-Za-z0-9_+.-]{1,160}$`)
var device = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\..*)?$`)

func ExactVersion(v string) bool {
	return token.MatchString(v) && v != "." && v != ".." && v != "latest" && v != "release" && !strings.Contains(v, "SNAPSHOT")
}
func SafePath(p string) error {
	if p == "" || len(p) > 2048 || strings.ContainsAny(p, "\\:<>\"|?*\x00") || strings.HasPrefix(p, "/") {
		return errors.New("unsafe artifact path")
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, " .") != part || device.MatchString(part) {
			return errors.New("unsafe artifact path")
		}
		for _, r := range part {
			if r < 32 {
				return errors.New("unsafe artifact path")
			}
		}
	}
	return nil
}
func ValidURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" || strings.Contains(u.Host, ":") {
		return errors.New("unsafe publisher URL")
	}
	switch u.Host {
	case "meta.fabricmc.net", "maven.fabricmc.net", "maven.minecraftforge.net", "maven.neoforged.net", "libraries.minecraft.net", "piston-data.mojang.com", "piston-meta.mojang.com", "launcher.mojang.com":
	default:
		return errors.New("unapproved publisher host")
	}
	if e = SafePath(strings.TrimPrefix(u.Path, "/")); e != nil {
		return e
	}
	return nil
}
func Digest(algorithm string, b []byte) string {
	if algorithm == "sha1" {
		h := sha1.Sum(b)
		return hex.EncodeToString(h[:])
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ValidDigest(algorithm, value string) bool {
	n := 64
	if algorithm == "sha1" {
		n = 40
	} else if algorithm != "sha256" {
		return false
	}
	b, e := hex.DecodeString(value)
	return e == nil && len(b)*2 == n && strings.ToLower(value) == value
}

type Source struct {
	URL         string    `json:"url"`
	SHA256      string    `json:"sha256"`
	RetrievedAt time.Time `json:"retrieved_at"`
}
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string {
	return fmt.Sprintf("official publisher returned HTTP %d; obtain missing files from the official publisher download page", e.Status)
}

type Client struct{ http *http.Client }

func NewClient(in *http.Client) *Client {
	var c http.Client
	if in != nil {
		c = *in
	}
	c.Jar = nil
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("publisher redirects are refused") }
	if c.Timeout == 0 || c.Timeout > 2*time.Minute {
		c.Timeout = 2 * time.Minute
	}
	return &Client{&c}
}
func (c *Client) request(ctx context.Context, method, u string) (*http.Response, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if e := ValidURL(u); e != nil {
		return nil, e
	}
	r, e := http.NewRequestWithContext(ctx, method, u, nil)
	if e != nil {
		return nil, errors.New("create publisher request")
	}
	r.Header.Set("User-Agent", UserAgent)
	r.Header.Set("Accept-Encoding", "identity")
	resp, e := c.http.Do(r)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("official publisher request failed")
	}
	if resp.StatusCode != 200 {
		_ = resp.Body.Close()
		return nil, &HTTPError{resp.StatusCode}
	}
	return resp, nil
}
func (c *Client) Bytes(ctx context.Context, u string, max int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, e := c.request(ctx, "GET", u)
	if e != nil {
		return nil, e
	}
	defer r.Body.Close()
	b, e := io.ReadAll(io.LimitReader(r.Body, max+1))
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("read publisher response")
	}
	if int64(len(b)) > max {
		return nil, errors.New("publisher response exceeds limit")
	}
	return b, nil
}
func (c *Client) JSON(ctx context.Context, u string, out any) (Source, error) {
	b, e := c.Bytes(ctx, u, 8<<20)
	s := Source{URL: u, SHA256: Digest("sha256", b), RetrievedAt: time.Now().UTC()}
	if e != nil {
		return s, e
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if dec.Decode(out) != nil {
		return s, errors.New("malformed publisher metadata")
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF {
		return s, errors.New("trailing publisher metadata")
	}
	return s, nil
}
func (c *Client) Size(ctx context.Context, u string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, e := c.request(ctx, "HEAD", u)
	if e != nil {
		return 0, e
	}
	defer r.Body.Close()
	if r.ContentLength <= 0 || r.ContentLength > MaxArtifact {
		return 0, errors.New("publisher artifact has no bounded length")
	}
	return r.ContentLength, nil
}
func (c *Client) Checksum(ctx context.Context, u string) (domain.Checksum, []Source, error) {
	var sources []Source
	for _, algorithm := range []string{"sha256", "sha1"} {
		link := u + "." + algorithm
		b, e := c.Bytes(ctx, link, 256)
		if e != nil {
			var status *HTTPError
			if errors.As(e, &status) && status.Status == 404 {
				continue
			}
			return domain.Checksum{}, nil, e
		}
		v := strings.TrimSpace(string(b))
		if !ValidDigest(algorithm, v) {
			return domain.Checksum{}, nil, errors.New("malformed published Maven checksum")
		}
		sources = append(sources, Source{URL: link, SHA256: Digest("sha256", b), RetrievedAt: time.Now().UTC()})
		return domain.Checksum{Algorithm: algorithm, Value: v}, sources, nil
	}
	return domain.Checksum{}, nil, errors.New("required published checksum unavailable; use the official publisher download page")
}
func (c *Client) Artifact(ctx context.Context, provider, coordinate, base string) (domain.Artifact, []Source, error) {
	p, e := MavenPath(coordinate)
	if e != nil {
		return domain.Artifact{}, nil, e
	}
	u := strings.TrimSuffix(base, "/") + "/" + p
	checksum, sources, e := c.Checksum(ctx, u)
	if e != nil {
		return domain.Artifact{}, nil, e
	}
	size, e := c.Size(ctx, u)
	if e != nil {
		return domain.Artifact{}, nil, e
	}
	return domain.Artifact{ID: coordinate, Provider: provider, VersionID: coordinate, Filename: path.Base(p), SourceURL: u, Size: size, Checksum: checksum, RetrievedAt: time.Now().UTC()}, sources, nil
}
func MavenPath(coordinate string) (string, error) {
	ext := "jar"
	p := strings.Split(coordinate, "@")
	if len(p) > 2 {
		return "", errors.New("invalid Maven coordinate")
	}
	if len(p) == 2 {
		ext = p[1]
	}
	parts := strings.Split(p[0], ":")
	if len(parts) < 3 || len(parts) > 4 {
		return "", errors.New("invalid Maven coordinate")
	}
	for _, s := range append(parts, ext) {
		if !ExactVersion(s) {
			return "", errors.New("unsafe Maven coordinate")
		}
	}
	name := parts[1] + "-" + parts[2]
	if len(parts) == 4 {
		name += "-" + parts[3]
	}
	out := strings.ReplaceAll(parts[0], ".", "/") + "/" + parts[1] + "/" + parts[2] + "/" + name + "." + ext
	if e := SafePath(out); e != nil {
		return "", e
	}
	return out, nil
}
func (c *Client) Download(ctx context.Context, a domain.Artifact, w io.Writer) (string, error) {
	if a.Size <= 0 || a.Size > MaxArtifact || !ValidDigest(a.Checksum.Algorithm, a.Checksum.Value) {
		return "", errors.New("invalid pinned artifact")
	}
	r, e := c.request(ctx, "GET", a.SourceURL)
	if e != nil {
		return "", e
	}
	defer r.Body.Close()
	if r.ContentLength > 0 && r.ContentLength != a.Size {
		return "", errors.New("artifact response length mismatch")
	}
	var h hash.Hash = sha256.New()
	if a.Checksum.Algorithm == "sha1" {
		h = sha1.New()
	}
	local := sha256.New()
	n, e := io.Copy(io.MultiWriter(w, h, local), io.LimitReader(&contextReader{ctx, r.Body}, a.Size+1))
	if e != nil {
		return "", e
	}
	if n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.Checksum.Value {
		return "", errors.New("artifact hash or size mismatch")
	}
	return hex.EncodeToString(local.Sum(nil)), nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(b []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(b)
}
