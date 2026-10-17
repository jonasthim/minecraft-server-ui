package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func releaseListing(tag string) string {
	b, _ := json.Marshal(map[string]any{"tag_name": tag, "name": "Release title", "body": "<script>alert('untrusted')</script>\nNotes", "draft": false, "prerelease": false, "published_at": "2026-10-16T12:00:00+02:00", "html_url": "https://evil.example/", "assets": []any{}})
	return string(b)
}

func discoverySource(t *testing.T, f roundTrip) *Source {
	t.Helper()
	trust, _, _, _ := signedFixture(t)
	s, err := NewSource(trust)
	if err != nil {
		t.Fatal(err)
	}
	s.discoveryClient.Transport = f
	return s
}

func listingResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestDiscoverySourceUsesExactPublicAPIAndConstructedLink(t *testing.T) {
	s := discoverySource(t, roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.String() != "https://api.github.com/repos/example/manager/releases/latest" || r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" || r.Header.Get("User-Agent") == "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatalf("wrong request: %v %v", r.URL, r.Header)
		}
		return listingResponse(200, releaseListing("v1.10.0")), nil
	}))
	r, err := s.Latest(context.Background())
	if err != nil || r == nil {
		t.Fatal(r, err)
	}
	if r.URL != "https://github.com/example/manager/releases/tag/v1.10.0" || r.Version != "v1.10.0" || r.Title != "Release title" || !strings.Contains(r.Notes, "<script>") || r.NotesTruncated || r.PublishedAt.Format(time.RFC3339) != "2026-10-16T10:00:00Z" {
		t.Fatal(r)
	}
}

func TestDiscoverySourceRejectsUnsafeListingsAndDistinguishesEmpty(t *testing.T) {
	for _, body := range []string{
		releaseListing("v1.2.3-rc1"), releaseListing("1.2.3"), releaseListing("v01.2.3"), releaseListing("v18446744073709551616.0.0"), releaseListing("../v1.2.3"),
		strings.Replace(releaseListing("v1.2.3"), `"draft":false`, `"draft":true`, 1),
		strings.Replace(releaseListing("v1.2.3"), `"prerelease":false`, `"prerelease":true`, 1),
		strings.Replace(releaseListing("v1.2.3"), `"draft":false,`, ``, 1),
		strings.Replace(releaseListing("v1.2.3"), `"draft":false`, `"draft":false,"draft":true`, 1),
		strings.Replace(releaseListing("v1.2.3"), `2026-10-16T12:00:00+02:00`, `invalid`, 1),
		`null`, `[]`, releaseListing("v1.2.3") + `{}`, strings.Repeat("x", (1<<20)+1),
		strings.Replace(releaseListing("v1.2.3"), "Release title", string([]byte{0xff}), 1),
		strings.Replace(releaseListing("v1.2.3"), `"assets":[]`, `"assets":`+strings.Repeat("[", 20)+`0`+strings.Repeat("]", 20), 1),
	} {
		s := discoverySource(t, roundTrip(func(*http.Request) (*http.Response, error) { return listingResponse(200, body), nil }))
		if _, err := s.Latest(context.Background()); err == nil {
			t.Fatalf("accepted unsafe listing %.120s", body)
		}
	}
	for _, status := range []int{404, 403, 429, 500} {
		s := discoverySource(t, roundTrip(func(*http.Request) (*http.Response, error) {
			return listingResponse(status, `secret upstream error`), nil
		}))
		r, err := s.Latest(context.Background())
		if status == 404 {
			if r != nil || err != nil {
				t.Fatal(r, err)
			}
		} else if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal(err)
		}
	}
	s := discoverySource(t, roundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("https://evil.example/?secret=credential")
	}))
	if _, err := s.Latest(context.Background()); err == nil || strings.Contains(err.Error(), "credential") {
		t.Fatal(err)
	}
}

func TestDiscoveryNotesTruncateAtValidTextBoundary(t *testing.T) {
	body := strings.Replace(releaseListing("v1.2.3"), `"Release title"`, `"`+strings.Repeat("é", 300)+`"`, 1)
	var listing map[string]any
	_ = json.Unmarshal([]byte(body), &listing)
	listing["body"] = strings.Repeat("é", 40000)
	encoded, _ := json.Marshal(listing)
	body = string(encoded)
	s := discoverySource(t, roundTrip(func(*http.Request) (*http.Response, error) { return listingResponse(200, body), nil }))
	r, err := s.Latest(context.Background())
	if err != nil || !r.NotesTruncated || len(r.Notes) > 65536 || len(r.Title) > 256 || !json.Valid([]byte(`"`+r.Notes+`"`)) {
		t.Fatal(r, err)
	}
}

func TestDiscoveryRedirectsCannotEscapeExactEndpoint(t *testing.T) {
	s := discoverySource(t, nil)
	initial, _ := http.NewRequest("GET", "https://api.github.com/repos/example/manager/releases/latest", nil)
	for _, target := range []string{"https://api.github.com/repos/other/manager/releases/latest", "https://api.github.com/repos/example/manager/releases/latest?secret=x", "http://api.github.com/repos/example/manager/releases/latest", "https://api.github.com:443/repos/example/manager/releases/latest", "https://user@api.github.com/repos/example/manager/releases/latest", "https://github.com/example/manager", "https://127.0.0.1/", "https://api.github.com/repos/example/manager/releases/latest#secret"} {
		r, _ := http.NewRequest("GET", target, nil)
		if err := s.discoveryClient.CheckRedirect(r, []*http.Request{initial}); err == nil {
			t.Fatal("allowed", target)
		}
	}
	r, _ := http.NewRequest("GET", initial.URL.String(), nil)
	r.Header.Set("Authorization", "secret")
	r.Header.Set("Cookie", "secret")
	r.Header.Set("Referer", "secret")
	if err := s.discoveryClient.CheckRedirect(r, []*http.Request{initial}); err != nil {
		t.Fatal(err)
	}
	if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" {
		t.Fatal(r.Header)
	}
	if err := s.discoveryClient.CheckRedirect(r, []*http.Request{initial, initial, initial, initial}); err == nil {
		t.Fatal("unbounded redirects")
	}
	if err := releaseURL(initial.URL.String(), "https://github.com/example/manager/releases/download/v1.2.3/release.json", false); err == nil {
		t.Fatal("artifact redirect escaped to API")
	}
}

func TestDiscoveryTransportRejectsOtherDestinationsBeforeDial(t *testing.T) {
	s := discoverySource(t, nil)
	transport := newDiscoveryClient().Transport.(*http.Transport)
	for _, address := range []string{"127.0.0.1:443", "api.github.com:80", "api.github.com:444", "github.com:443", "release-assets.githubusercontent.com:443", "evil.example:443"} {
		if conn, err := transport.DialContext(context.Background(), "tcp", address); err == nil {
			_ = conn.Close()
			t.Fatal("allowed destination", address)
		}
	}
	assetTransport := s.client.Transport.(*http.Transport)
	if conn, err := assetTransport.DialContext(context.Background(), "tcp", "api.github.com:443"); err == nil {
		_ = conn.Close()
		t.Fatal("artifact dial allowed API")
	}
}

func discoveryService(t *testing.T, current string, f roundTrip) (*Service, domain.User) {
	t.Helper()
	database, err := db.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	actor, err := db.NewUsers(database).Create(context.Background(), db.NewUser{Username: "admin", Role: domain.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.NewUsers(database).Create(context.Background(), db.NewUser{Username: "other-admin", Role: domain.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	trust, _, _, _ := signedFixture(t)
	s := New(Config{Trust: trust, CurrentVersion: current, DB: database})
	s.source.discoveryClient.Transport = f
	return s, *actor
}

func TestDiscoveryExactVersionComparison(t *testing.T) {
	for _, tc := range []struct{ current, latest, comparison string }{
		{"v1.9.99", "v1.10.0", "newer"}, {"v1.10.0", "v1.9.99", "older"}, {"v1.2.3", "v1.2.3", "same"}, {"0.0.0-dev", "v1.2.3", "unknown"}, {"unknown", "v1.2.3", "unknown"}, {"v18446744073709551616.1.0", "v1.2.3", "unknown"}, {"v18446744073709551615.0.0", "v1.2.3", "older"},
	} {
		s, actor := discoveryService(t, tc.current, roundTrip(func(*http.Request) (*http.Response, error) {
			return listingResponse(200, releaseListing(tc.latest)), nil
		}))
		r, err := s.Discover(context.Background(), actor)
		if err != nil || r.Comparison != tc.comparison || r.CurrentVersion != tc.current || r.Cached || r.CheckedAt.IsZero() {
			t.Fatal(tc, r, err)
		}
	}
}

func TestDiscoveryCachesEmptyAndReleaseUntilExpiryButNeverErrors(t *testing.T) {
	for _, status := range []int{200, 404, 500} {
		calls := 0
		s, actor := discoveryService(t, "v1.2.2", roundTrip(func(*http.Request) (*http.Response, error) {
			calls++
			return listingResponse(status, releaseListing("v1.2.3")), nil
		}))
		now := fixtureNow
		s.now = func() time.Time { return now }
		first, err := s.Discover(context.Background(), actor)
		if status == 500 {
			if err == nil {
				t.Fatal("error hidden")
			}
			_, _ = s.Discover(context.Background(), actor)
			if calls != 2 {
				t.Fatal("error cached")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if status == 404 && (first.Release != nil || first.Comparison != "no_release") {
			t.Fatal(first)
		}
		if first.Release != nil {
			first.Release.Title = "caller mutated cache"
		}
		now = now.Add(5*time.Minute - time.Nanosecond)
		cached, err := s.Discover(context.Background(), actor)
		if err != nil || !cached.Cached || !cached.CheckedAt.Equal(fixtureNow) || calls != 1 || cached.Release != nil && cached.Release.Title == "caller mutated cache" {
			t.Fatal(cached, calls, err)
		}
		now = now.Add(time.Nanosecond)
		fresh, err := s.Discover(context.Background(), actor)
		if err != nil || fresh.Cached || calls != 2 {
			t.Fatal(fresh, calls, err)
		}
		if _, err := s.cfg.DB.Exec(`UPDATE users SET disabled=1 WHERE id=?`, actor.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Discover(context.Background(), actor); err == nil || calls != 2 {
			t.Fatal("cached disclosure after revocation", err)
		}
	}
}

func TestDiscoveryCoalescesWithoutActivationMutexAndCancelsWaiter(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s, actor := discoveryService(t, "v1.2.2", roundTrip(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > 21*time.Second {
			t.Error("remote discovery not bounded")
		}
		select {
		case <-release:
			return listingResponse(200, releaseListing("v1.2.3")), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}))
	s.mu.Lock() // Discovery must never wait for the staging/activation mutex.
	defer s.mu.Unlock()
	var wg sync.WaitGroup
	errch := make(chan error, 12)
	wg.Add(1)
	go func() { defer wg.Done(); _, err := s.Discover(context.Background(), actor); errch <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("discovery held activation mutex")
	}
	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() { _, err := s.Discover(waiterCtx, actor); waiterDone <- err }()
	select {
	case err := <-waiterDone:
		t.Fatalf("waiter returned before shared discovery: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	cancelWaiter()
	select {
	case err := <-waiterDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting request ignored cancellation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Discover(ctx, actor); err == nil {
		t.Fatal("canceled waiter succeeded")
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Discover(context.Background(), actor); errch <- err }()
	}
	close(release)
	wg.Wait()
	close(errch)
	for err := range errch {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("concurrent remote discovery", calls.Load())
	}
}

func TestDiscoveryCancellationAndRoleGates(t *testing.T) {
	entered := make(chan struct{})
	s, actor := discoveryService(t, "v1.2.2", roundTrip(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	for _, role := range []domain.Role{domain.RoleViewer, domain.RoleOperator} {
		if _, err := s.Discover(context.Background(), domain.User{Role: role}); err == nil {
			t.Fatal("role allowed", role)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Discover(ctx, actor); done <- err }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("remote request ignored cancellation")
	}
	if _, err := New(Config{}).Discover(context.Background(), actor); err == nil {
		t.Fatal("empty trust enabled discovery")
	}
}

func TestDiscoveryExpiredSuccessNeverMasksFailure(t *testing.T) {
	status, calls := 200, 0
	s, actor := discoveryService(t, "v1.2.3", roundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		return listingResponse(status, releaseListing("v1.2.3")), nil
	}))
	now := fixtureNow
	s.now = func() time.Time { return now }
	if first, err := s.Discover(context.Background(), actor); err != nil || first.Comparison != "same" {
		t.Fatal(first, err)
	}
	now = now.Add(6 * time.Minute)
	status = 500
	for i := 0; i < 2; i++ {
		result, err := s.Discover(context.Background(), actor)
		if err == nil || result.Comparison != "" || result.Release != nil {
			t.Fatal("stale success hid error", result, err)
		}
	}
	if calls != 3 {
		t.Fatal(calls)
	}
}
