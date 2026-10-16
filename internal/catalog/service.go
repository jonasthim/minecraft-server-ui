package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	MaxMetadataBytes       = 8 << 20
	MaxArtifactBytes int64 = 2 << 30
	MaxCacheBytes          = 32 << 20
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+~-]{0,159}$`)

type Config struct {
	DB          *sql.DB
	ArtifactDir string
	UserAgent   string
	Transport   http.RoundTripper
}
type cacheEntry struct {
	body    []byte
	fetched time.Time
}
type Service struct {
	db             *sql.DB
	dir, userAgent string
	client         *http.Client
	mu             sync.Mutex
	cache          map[string]cacheEntry
	cacheBytes     int
	epoch          uint64
	slots          chan struct{}
}

func New(c Config) (*Service, error) {
	if c.DB == nil || !filepath.IsAbs(c.ArtifactDir) || strings.TrimSpace(c.UserAgent) == "" || strings.ContainsAny(c.UserAgent, "\r\n") {
		return nil, domain.E(domain.CodeValidationFailed, "catalog requires database, absolute artifact directory and user-agent")
	}
	if e := os.MkdirAll(c.ArtifactDir, 0700); e != nil {
		return nil, fmt.Errorf("catalog artifact directory: %w", e)
	}
	fi, e := os.Lstat(c.ArtifactDir)
	if e != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return nil, domain.E(domain.CodeValidationFailed, "unsafe catalog artifact directory")
	}
	tr := c.Transport
	if tr == nil {
		tr = http.DefaultTransport.(*http.Transport).Clone()
	}
	s := &Service{db: c.DB, dir: c.ArtifactDir, userAgent: c.UserAgent, cache: map[string]cacheEntry{}, slots: make(chan struct{}, 2)}
	s.client = &http.Client{Transport: tr, Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return domain.E(domain.CodeUpstreamError, "catalog redirects require manual resolution")
	}}
	return s, nil
}
func validID(id string) bool        { return idPattern.MatchString(id) && !strings.Contains(id, "..") }
func validProvider(p Provider) bool { return p == Modrinth || p == CurseForge || p == Hangar }
func invalid(message string) error  { return domain.E(domain.CodeValidationFailed, message) }
func upstream(message string) error { return domain.E(domain.CodeUpstreamError, message) }
func normalize(q Query) (Query, error) {
	if !validProvider(q.Provider) || q.Kind != "" && q.Kind != Mod && q.Kind != Plugin && q.Kind != Modpack {
		return q, invalid("unknown catalog provider or kind")
	}
	if q.Kind == "" {
		q.Kind = Mod
	}
	if q.Provider == Hangar && q.Kind != Plugin || q.Provider == CurseForge && q.Kind == Plugin {
		return q, invalid("provider does not offer this content kind")
	}
	if len(q.Text) > 200 || strings.ContainsAny(q.Text, "\x00\r\n") || q.Minecraft != "" && !validID(q.Minecraft) || q.Loader != "" && !contains([]string{"fabric", "forge", "neoforge", "paper", "bukkit", "spigot", "purpur", "quilt"}, q.Loader) {
		return q, invalid("invalid catalog filters")
	}
	if q.Offset < 0 || q.Offset > 10000 || q.Limit < 0 || q.Limit > 50 {
		return q, invalid("catalog page outside limits")
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Provider == Hangar && q.Limit > 25 {
		q.Limit = 25
	}
	return q, nil
}
func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
func (s *Service) Providers(ctx context.Context) ([]Availability, error) {
	var key string
	e := s.db.QueryRowContext(ctx, "SELECT curseforge_key FROM catalog_settings WHERE id=1").Scan(&key)
	if e != nil {
		return nil, fmt.Errorf("read catalog availability: %w", e)
	}
	reason := ""
	if key == "" {
		reason = "An administrator must supply a private CurseForge API key."
	}
	return []Availability{{Modrinth, true, ""}, {CurseForge, key != "", reason}, {Hangar, true, ""}}, nil
}
func (s *Service) ConfigureCurseForge(ctx context.Context, actor domain.User, key string) (Availability, error) {
	if actor.Disabled || actor.Role != domain.RoleAdmin {
		return Availability{}, domain.E(domain.CodeForbidden, "administrator required")
	}
	if len(key) > 512 || strings.TrimSpace(key) != key || strings.ContainsAny(key, "\r\n\x00") {
		return Availability{}, invalid("invalid CurseForge key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Availability{}, fmt.Errorf("configure catalog: %w", e)
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, "UPDATE catalog_settings SET curseforge_key=?,updated_at=? WHERE id=1", key, time.Now().UTC().Format(time.RFC3339Nano))
	if e != nil {
		return Availability{}, fmt.Errorf("store catalog configuration: %w", e)
	}
	action := "catalog.curseforge_key.replace"
	if key == "" {
		action = "catalog.curseforge_key.clear"
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO audit_log(ts,user_id,username,action,target,details_json) VALUES(?,?,?,?,?,?)", time.Now().UTC().Format(time.RFC3339Nano), actor.ID, actor.Username, action, "curseforge", "{}")
	if e != nil {
		return Availability{}, fmt.Errorf("audit catalog configuration: %w", e)
	}
	if e = tx.Commit(); e != nil {
		return Availability{}, fmt.Errorf("commit catalog configuration: %w", e)
	}
	s.epoch++
	s.cache = map[string]cacheEntry{}
	s.cacheBytes = 0
	a := Availability{Provider: CurseForge, Enabled: key != ""}
	if key == "" {
		a.Reason = "An administrator must supply a private CurseForge API key."
	}
	return a, nil
}
func base(p Provider) string {
	switch p {
	case Modrinth:
		return "https://api.modrinth.com/v2"
	case CurseForge:
		return "https://api.curseforge.com/v1"
	case Hangar:
		return "https://hangar.papermc.io/api/v1"
	}
	return ""
}
func (s *Service) metadata(ctx context.Context, p Provider, path string, q url.Values, out any) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return time.Time{}, domain.Wrap(domain.CodeUpstreamError, "catalog metadata canceled", ctx.Err())
	}
	s.mu.Lock()
	epoch := s.epoch
	s.mu.Unlock()
	key := ""
	if p == CurseForge {
		if e := s.db.QueryRowContext(ctx, "SELECT curseforge_key FROM catalog_settings WHERE id=1").Scan(&key); e != nil {
			return time.Time{}, fmt.Errorf("read catalog credentials: %w", e)
		}
		if key == "" {
			return time.Time{}, domain.E(domain.CodeUnavailable, "CurseForge requires an administrator-supplied private API key")
		}
	}
	u := base(p) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	cacheKey := fmt.Sprintf("%d:%s", epoch, u)
	s.mu.Lock()
	entry, ok := s.cache[cacheKey]
	s.mu.Unlock()
	if !ok || time.Since(entry.fetched) > 5*time.Minute {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if e != nil {
			return time.Time{}, upstream("invalid publisher metadata request")
		}
		req.Header.Set("User-Agent", s.userAgent)
		req.Header.Set("Accept", "application/json")
		if key != "" {
			req.Header.Set("x-api-key", key)
		}
		resp, e := s.client.Do(req)
		if e != nil {
			if ctx.Err() != nil {
				return time.Time{}, domain.Wrap(domain.CodeUpstreamError, "publisher metadata deadline or cancellation", ctx.Err())
			}
			return time.Time{}, upstream("publisher metadata request failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode == 404 {
			return time.Time{}, domain.NotFound("catalog item")
		}
		if resp.StatusCode != 200 {
			return time.Time{}, upstream("publisher metadata unavailable or access refused")
		}
		b, e := io.ReadAll(io.LimitReader(resp.Body, MaxMetadataBytes+1))
		if e != nil || len(b) > MaxMetadataBytes {
			return time.Time{}, upstream("publisher metadata exceeds limits or is incomplete")
		}
		if !json.Valid(b) || bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
			return time.Time{}, upstream("malformed publisher metadata")
		}
		entry = cacheEntry{b, time.Now().UTC()}
		if e = json.Unmarshal(b, out); e != nil {
			return time.Time{}, upstream("invalid publisher metadata shape")
		}
		s.mu.Lock()
		if epoch == s.epoch {
			if old, exists := s.cache[cacheKey]; exists {
				s.cacheBytes -= len(old.body)
				delete(s.cache, cacheKey)
			}
			for len(s.cache) >= 256 || s.cacheBytes+len(b) > MaxCacheBytes {
				for k, v := range s.cache {
					s.cacheBytes -= len(v.body)
					delete(s.cache, k)
					break
				}
			}
			s.cache[cacheKey] = entry
			s.cacheBytes += len(b)
		}
		s.mu.Unlock()
		return entry.fetched, nil
	}
	if e := json.Unmarshal(entry.body, out); e != nil {
		return time.Time{}, upstream("invalid cached metadata")
	}
	return entry.fetched, nil
}
