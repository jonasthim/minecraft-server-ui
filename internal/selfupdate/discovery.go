package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

const discoveryTimeout = 20 * time.Second
const discoveryTTL = 5 * time.Minute
const maxDiscoveryBytes = 1 << 20
const maxNotesBytes = 65536

// Discovery is an unsigned publisher listing observation, never authorization to
// download or activate. The exact-version signed manifest remains authoritative.
type Discovery struct {
	CurrentVersion string          `json:"current_version"`
	CheckedAt      time.Time       `json:"checked_at"`
	Cached         bool            `json:"cached"`
	Comparison     string          `json:"comparison"`
	Release        *ReleaseSummary `json:"release"`
}

type ReleaseSummary struct {
	Version        string    `json:"version"`
	Title          string    `json:"title"`
	Notes          string    `json:"notes"`
	NotesTruncated bool      `json:"notes_truncated"`
	PublishedAt    time.Time `json:"published_at"`
	URL            string    `json:"url"`
}

type discoveryFlight struct {
	done   chan struct{}
	result Discovery
	err    error
}

type discoveryState struct {
	mu     sync.Mutex
	cached *Discovery
	flight *discoveryFlight
}

func newDiscoveryClient() *http.Client {
	return &http.Client{
		Transport: publicReleaseTransport(map[string]bool{"api.github.com": true}),
		Timeout:   discoveryTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Repository moves require reviewed trust configuration. Only an
			// identical, credential-free endpoint can survive a redirect.
			if len(via) == 0 || len(via) > 3 || req.URL.String() != via[0].URL.String() || req.URL.Scheme != "https" || req.URL.Host != "api.github.com" || req.URL.User != nil || req.URL.RawQuery != "" || req.URL.Fragment != "" {
				return errors.New("release discovery redirect is not permitted")
			}
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
			req.Header.Del("Referer")
			return nil
		},
	}
}

// Latest uses only the configured public repository and official latest-release
// endpoint. A 404 is an explicit empty observation; failures never become empty.
func (s *Source) Latest(ctx context.Context) (*ReleaseSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	endpoint := "https://api.github.com/repos/" + s.trust.Repository + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("cannot construct release discovery request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "minecraft-server-ui-updater/1")
	resp, err := s.discoveryClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("release discovery request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release discovery returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiscoveryBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("release discovery response could not be read")
	}
	if len(body) > maxDiscoveryBytes {
		return nil, errors.New("release discovery response exceeds size limit")
	}
	if !utf8.Valid(body) {
		return nil, errors.New("invalid release discovery text encoding")
	}
	// Ignore GitHub's other fields while refusing duplicate members, deep nesting,
	// trailing values and ambiguous required booleans in this untrusted JSON.
	d := json.NewDecoder(bytes.NewReader(body))
	if err := scanValue(d, 0); err != nil {
		return nil, errors.New("invalid release discovery response")
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("invalid release discovery response")
	}
	var listing struct {
		Tag         string    `json:"tag_name"`
		Title       string    `json:"name"`
		Notes       string    `json:"body"`
		Draft       *bool     `json:"draft"`
		Prerelease  *bool     `json:"prerelease"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := json.Unmarshal(body, &listing); err != nil || listing.Draft == nil || listing.Prerelease == nil || *listing.Draft || *listing.Prerelease || listing.PublishedAt.IsZero() {
		return nil, errors.New("release discovery did not identify a published stable release")
	}
	if _, err := ParseStable(listing.Tag); err != nil {
		return nil, errors.New("release discovery tag is not a stable version")
	}
	title := strings.TrimSpace(listing.Title)
	if title == "" {
		title = listing.Tag
	}
	return &ReleaseSummary{
		Version: listing.Tag, Title: truncateText(title, 256), Notes: truncateText(listing.Notes, maxNotesBytes),
		NotesTruncated: len(listing.Notes) > maxNotesBytes, PublishedAt: listing.PublishedAt.UTC(),
		URL: "https://github.com/" + s.trust.Repository + "/releases/tag/" + listing.Tag,
	}, nil
}

func truncateText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func cloneDiscovery(value Discovery) Discovery {
	if value.Release != nil {
		copy := *value.Release
		value.Release = &copy
	}
	return value
}

func (s *Service) Discover(ctx context.Context, actor domain.User) (Discovery, error) {
	if err := requireAdmin(actor); err != nil {
		return Discovery{}, err
	}
	if s.source == nil {
		return Discovery{}, domain.E(domain.CodeUnavailable, "Release trust is not configured.")
	}
	if err := ctx.Err(); err != nil {
		return Discovery{}, err
	}
	if _, err := s.freshAdmin(ctx, actor); err != nil {
		return Discovery{}, err
	}
	if err := ctx.Err(); err != nil {
		return Discovery{}, err
	}
	state := &s.discovery
	state.mu.Lock()
	if state.cached != nil && s.now().Sub(state.cached.CheckedAt) >= 0 && s.now().Sub(state.cached.CheckedAt) < discoveryTTL {
		out := cloneDiscovery(*state.cached)
		out.Cached = true
		state.mu.Unlock()
		return out, nil
	}
	if flight := state.flight; flight != nil {
		state.mu.Unlock()
		select {
		case <-ctx.Done():
			return Discovery{}, ctx.Err()
		case <-flight.done:
			if err := ctx.Err(); err != nil {
				return Discovery{}, err
			}
			if _, err := s.freshAdmin(ctx, actor); err != nil {
				return Discovery{}, err
			}
			if flight.err != nil {
				return Discovery{}, flight.err
			}
			return cloneDiscovery(flight.result), flight.err
		}
	}
	flight := &discoveryFlight{done: make(chan struct{})}
	state.flight = flight
	state.mu.Unlock()

	// Network work holds only this in-flight slot, never the staging mutex.
	release, err := s.source.Latest(ctx)
	out := Discovery{CurrentVersion: s.cfg.CurrentVersion, CheckedAt: s.now().UTC(), Comparison: "no_release", Release: release}
	if err != nil {
		err = domain.Wrap(domain.CodeUpstreamError, "Latest stable release could not be checked. Retry the discovery request.", err)
	}
	if release != nil {
		out.Comparison = "unknown"
		current, e := ParseStable(out.CurrentVersion)
		if e == nil {
			candidate, _ := ParseStable(release.Version)
			switch {
			case candidate == current:
				out.Comparison = "same"
			case newer(candidate, current):
				out.Comparison = "newer"
			default:
				out.Comparison = "older"
			}
		}
	}
	// Recheck authority after remote work before returning publisher metadata.
	if err == nil {
		_, err = s.freshAdmin(ctx, actor)
	}
	state.mu.Lock()
	if err == nil {
		copy := cloneDiscovery(out)
		state.cached = &copy
	}
	flight.result, flight.err = cloneDiscovery(out), err
	state.flight = nil
	close(flight.done)
	state.mu.Unlock()
	if err != nil {
		return Discovery{}, err
	}
	return out, nil
}
