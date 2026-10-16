package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func service(t *testing.T, fn transportFunc) *Service {
	t.Helper()
	d, e := db.OpenMemory(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { d.Close() })
	s, e := New(Config{DB: d, ArtifactDir: t.TempDir(), UserAgent: "catalog-test/contact", Transport: fn})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestAbsentKeyNeverContactsCurseForge(t *testing.T) {
	s := service(t, func(*http.Request) (*http.Response, error) { t.Fatal("credential-free CF request"); return nil, nil })
	_, e := s.Search(context.Background(), Query{Provider: CurseForge, Kind: Mod, Limit: 10})
	if domain.AsError(e).Code != domain.CodeUnavailable {
		t.Fatalf("%v", e)
	}
}
func TestModrinthSearchFiltersPagingAndCache(t *testing.T) {
	calls := 0
	s := service(t, func(r *http.Request) (*http.Response, error) {
		calls++
		q := r.URL.Query()
		if q.Get("offset") != "5" || q.Get("limit") != "2" || !strings.Contains(q.Get("facets"), "categories:paper") || r.UserAgent() == "" {
			t.Fatalf("request %v", r.URL)
		}
		return response(`{"hits":[{"project_id":"abcd1234","slug":"example","project_type":"mod","title":"Example","description":"Example plugin","server_side":"required","categories":["paper"],"versions":["1.21.1"]}],"total_hits":7}`), nil
	})
	q := Query{Provider: Modrinth, Kind: Plugin, Loader: "paper", Minecraft: "1.21.1", Offset: 5, Limit: 2}
	for range 2 {
		v, e := s.Search(context.Background(), q)
		if e != nil || len(v.Projects) != 1 || v.Projects[0].Kind != Plugin || v.Total != 7 || v.FetchedAt.IsZero() {
			t.Fatalf("%+v %v", v, e)
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
func TestModrinthVersionSideAndDependencies(t *testing.T) {
	s := service(t, func(r *http.Request) (*http.Response, error) {
		return response(`{"id":"v123","project_id":"p123","name":"client release","version_number":"1.0","environment":"client_only","game_versions":["1.21.1"],"loaders":["fabric"],"files":[{"id":"f123","filename":"example.jar","url":"https://cdn.modrinth.com/data/p123/versions/v123/example.jar","size":3,"hashes":{"sha512":"` + strings.Repeat("a", 128) + `"}}],"dependencies":[{"project_id":"dep123","version_id":"d123","dependency_type":"required"}]}`), nil
	})
	v, e := s.Version(context.Background(), Modrinth, "p123", "v123")
	if e != nil || v.Compatibility.ServerSide != "unsupported" || len(v.Dependencies) != 1 || v.Dependencies[0].Relation != "required" || v.Files[0].ID != "f123" {
		t.Fatalf("%+v %v", v, e)
	}
}
func TestMalformedTimeoutAndUnsafeInput(t *testing.T) {
	for _, body := range []string{`null`, `{"hits":null}`, `{broken`, strings.Repeat(" ", 8<<20) + `{}`} {
		s := service(t, func(*http.Request) (*http.Response, error) { return response(body), nil })
		if _, e := s.Search(context.Background(), Query{Provider: Modrinth, Kind: Mod}); e == nil {
			t.Fatal("accepted malformed response")
		}
	}
	s := service(t, func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, e := s.Project(ctx, Modrinth, "example"); e == nil {
		t.Fatal("timeout accepted")
	}
	for _, id := range []string{"../bad", "bad%2fpath", "a?secret=1", "bad\\path"} {
		if _, e := s.Project(context.Background(), Modrinth, id); domain.AsError(e).Code != domain.CodeValidationFailed {
			t.Fatal(e)
		}
	}
}
func TestKeyAdminStorageAndClear(t *testing.T) {
	s := service(t, func(*http.Request) (*http.Response, error) {
		return response(`{"data":[],"pagination":{"totalCount":0}}`), nil
	})
	ctx := context.Background()
	if _, e := s.ConfigureCurseForge(ctx, domain.User{Role: domain.RoleOperator}, "private-key"); domain.AsError(e).Code != domain.CodeForbidden {
		t.Fatal(e)
	}
	a, e := s.ConfigureCurseForge(ctx, domain.User{Role: domain.RoleAdmin}, "private-key")
	if e != nil || !a.Enabled {
		t.Fatal(a, e)
	}
	a, e = s.ConfigureCurseForge(ctx, domain.User{Role: domain.RoleAdmin}, "")
	if e != nil || a.Enabled {
		t.Fatal(a, e)
	}
}
func TestVerifiedDownloadMismatchDoesNotPublish(t *testing.T) {
	sum := sha256.Sum256([]byte("abc"))
	f := File{ID: "f1", Filename: "example.jar", URL: "https://cdn.modrinth.com/data/p/v/example.jar", Size: 3, Hashes: map[string]string{"sha256": hex.EncodeToString(sum[:])}}
	s := service(t, func(*http.Request) (*http.Response, error) { return response("bad"), nil })
	if _, e := s.downloadFile(context.Background(), Selection{Provider: Modrinth, ProjectID: "p", VersionID: "v", FileID: "f1"}, f, Compatibility{}); e == nil {
		t.Fatal("hash mismatch accepted")
	}
	s.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) { return response("abc"), nil })
	a, e := s.downloadFile(context.Background(), Selection{Provider: Modrinth, ProjectID: "p", VersionID: "v", FileID: "f1"}, f, Compatibility{})
	if e != nil || a.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal(a, e)
	}
}
func TestDownloadURLPolicy(t *testing.T) {
	for _, u := range []string{"http://cdn.modrinth.com/a", "https://cdn.modrinth.com.evil/a", "https://secret@cdn.modrinth.com/a", "https://cdn.modrinth.com:443/a", "https://cdn.modrinth.com/a?token=private", "https://127.0.0.1/a", "https://cdn.modrinth.com/a/../b", "https://cdn.modrinth.com/%2e%2e/b"} {
		if validArtifactURL(Modrinth, u) {
			t.Fatal(u)
		}
	}
	if !validArtifactURL(Modrinth, "https://cdn.modrinth.com/data/p/v/a.jar") {
		t.Fatal("official CDN rejected")
	}
}

func TestCurseForgeNullableDownloadAndServerPackEvidence(t *testing.T) {
	s := service(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("x-api-key") != "test-private" {
			t.Fatal("missing private credential")
		}
		return response(`{"data":{"id":88,"modId":42,"displayName":"Pack","fileName":"pack.zip","fileLength":200,"isAvailable":true,"downloadUrl":null,"isServerPack":false,"serverPackFileId":99,"gameVersions":["1.21.1","NeoForge"],"hashes":[{"algo":1,"value":"` + strings.Repeat("b", 40) + `"}],"dependencies":[{"modId":7,"relationType":3}]}}`), nil
	})
	_, e := s.ConfigureCurseForge(context.Background(), domain.User{Role: domain.RoleAdmin}, "test-private")
	if e != nil {
		t.Fatal(e)
	}
	v, e := s.Version(context.Background(), CurseForge, "42", "88")
	if e != nil || v.IsServerPack || v.ServerPackFileID != "99" || v.Compatibility.ServerSide != "unknown" || v.Files[0].URL != "" || v.Files[0].ManualReason == "" || !v.Dependencies[0].Required {
		t.Fatalf("%+v %v", v, e)
	}
}
func TestHangarPlatformAndExternalRequiredDependency(t *testing.T) {
	s := service(t, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/versions/") {
			return response(`{"id":123,"projectId":31,"name":"5.0+1","platformDependencies":{"PAPER":["1.21.1"],"VELOCITY":["3.4"]},"downloads":{"PAPER":{"downloadUrl":"https://hangarcdn.papermc.io/p.jar","fileInfo":{"name":"p.jar","sizeBytes":3,"sha256Hash":"` + strings.Repeat("c", 64) + `"}},"VELOCITY":{"externalUrl":"https://unknown.example/plugin"}},"pluginDependencies":{"PAPER":[{"name":"External plugin","required":true,"externalUrl":"https://publisher.example/plugin","platform":"PAPER"}]}}`), nil
		}
		return response(`{"id":31,"name":"Example","namespace":{"owner":"Author","slug":"Example"},"supportedPlatforms":{"PAPER":["1.21.1"]}}`), nil
	})
	v, e := s.Version(context.Background(), Hangar, "31", "5.0+1")
	if e != nil || v.UpstreamID != "123" || v.Number != "5.0+1" || len(v.Files) != 1 || v.Files[0].ID != "PAPER" || !v.Dependencies[0].Required || v.Dependencies[0].ManualURL != "https://publisher.example/plugin" || strings.Contains(strings.Join(v.Compatibility.Minecraft, ","), "3.4") {
		t.Fatalf("%+v %v", v, e)
	}
	if v.OfficialURL != "https://hangar.papermc.io/Author/Example/versions/5.0+1" {
		t.Fatal(v.OfficialURL)
	}
}
func TestCredentialRedirectNeverFollows(t *testing.T) {
	calls := 0
	s := service(t, func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://attacker.example/steal"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	_, e := s.ConfigureCurseForge(context.Background(), domain.User{Role: domain.RoleAdmin}, "test-private")
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Search(context.Background(), Query{Provider: CurseForge, Kind: Mod})
	if e == nil || calls != 1 || strings.Contains(e.Error(), "test-private") || strings.Contains(e.Error(), "attacker") {
		t.Fatal(calls, e)
	}
}
func TestDownloadRejectsTraversalAndMissingIntegrity(t *testing.T) {
	for _, f := range []File{{ID: "x", Filename: "../x.jar", URL: "https://cdn.modrinth.com/x.jar", Size: 3, Hashes: map[string]string{"sha256": strings.Repeat("a", 64)}}, {ID: "x", Filename: "x.jar", URL: "https://cdn.modrinth.com/x.jar", Size: 3, Hashes: map[string]string{"md5": strings.Repeat("a", 32)}}} {
		s := service(t, func(*http.Request) (*http.Response, error) { t.Fatal("unsafe file fetched"); return nil, nil })
		if _, e := s.downloadFile(context.Background(), Selection{Provider: Modrinth}, f, Compatibility{}); e == nil {
			t.Fatal("unsafe file accepted")
		}
	}
}
func TestMetadataCacheAggregateBound(t *testing.T) {
	s := service(t, func(*http.Request) (*http.Response, error) {
		return response(`{"value":"` + strings.Repeat("a", 1<<20) + `"}`), nil
	})
	for i := range 40 {
		var out map[string]string
		if _, e := s.metadata(context.Background(), Modrinth, "/project/"+strconv.Itoa(i), nil, &out); e != nil {
			t.Fatal(e)
		}
	}
	if s.cacheBytes > 32<<20 || len(s.cache) > 32 {
		t.Fatal("cache unbounded", s.cacheBytes, len(s.cache))
	}
}

func TestRejectsWrongModrinthVersionIdentity(t *testing.T) {
	s := service(t, func(*http.Request) (*http.Response, error) {
		return response(`{"id":"different","project_id":"project","name":"wrong","files":[],"dependencies":[]}`), nil
	})
	if _, e := s.Version(context.Background(), Modrinth, "project", "requested"); e == nil {
		t.Fatal("accepted another version as requested version")
	}
}
func TestDownloadCancellationCleansOnlyOwnedStage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := service(t, func(*http.Request) (*http.Response, error) { cancel(); return response("abc"), nil })
	sentinel := filepath.Join(s.dir, "operator-file")
	if e := os.WriteFile(sentinel, []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	_, e := s.downloadFile(ctx, Selection{Provider: Modrinth}, File{ID: "f", Filename: "a.jar", URL: "https://cdn.modrinth.com/a.jar", Size: 3, Hashes: map[string]string{"sha256": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}}, Compatibility{})
	if e == nil {
		t.Fatal("canceled download published")
	}
	entries, e := os.ReadDir(s.dir)
	if e != nil || len(entries) != 1 || entries[0].Name() != "operator-file" {
		t.Fatal(entries, e)
	}
}
func TestExistingArtifactVerifiedAndNeverOverwritten(t *testing.T) {
	s := service(t, func(*http.Request) (*http.Response, error) { return response("abc"), nil })
	f := File{ID: "f", Filename: "a.jar", URL: "https://cdn.modrinth.com/a.jar", Size: 3, Hashes: map[string]string{"sha256": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}}
	sel := Selection{Provider: Modrinth}
	a, e := s.downloadFile(context.Background(), sel, f, Compatibility{})
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.downloadFile(context.Background(), sel, f, Compatibility{})
	if e != nil || b.Path != a.Path {
		t.Fatal(b, e)
	}
	if e = os.Chmod(a.Path, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(a.Path, []byte("bad"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.downloadFile(context.Background(), sel, f, Compatibility{}); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal("corrupt existing artifact silently replaced", e)
	}
	data, e := os.ReadFile(a.Path)
	if e != nil || string(data) != "bad" {
		t.Fatal("existing bytes changed", e)
	}
}
func TestDownloadRedirectNeverFollows(t *testing.T) {
	calls := 0
	s := service(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("x-api-key") != "" {
			t.Fatal("credential sent to CDN")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://127.0.0.1/private"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	_, e := s.downloadFile(context.Background(), Selection{Provider: Modrinth}, File{ID: "f", Filename: "a.jar", URL: "https://cdn.modrinth.com/a.jar", Size: 3, Hashes: map[string]string{"sha256": strings.Repeat("a", 64)}}, Compatibility{})
	if e == nil || calls != 1 {
		t.Fatal(calls, e)
	}
}

func TestPublishedArtifactLeavesNoStagingAlias(t *testing.T) {
	s := service(t, func(*http.Request) (*http.Response, error) { return response("abc"), nil })
	_, e := s.downloadFile(context.Background(), Selection{Provider: Modrinth}, File{ID: "f", Filename: "a.jar", URL: "https://cdn.modrinth.com/a.jar", Size: 3, Hashes: map[string]string{"sha256": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}}, Compatibility{})
	if e != nil {
		t.Fatal(e)
	}
	stages, e := filepath.Glob(filepath.Join(s.dir, ".catalog-stage-*"))
	if e != nil || len(stages) != 0 {
		t.Fatal("publication left staging files", stages, e)
	}
}
func TestWindowsUnsafePublisherFilenameRequiresManualResolution(t *testing.T) {
	for _, name := range []string{"CON.jar", "aux.zip", "a?.jar", "file.", "plugin\x01.jar", "COM1.jar", "name|bad.jar"} {
		f := normalizeFile(Modrinth, File{ID: "f", Filename: name, URL: "https://cdn.modrinth.com/a.jar", Size: 3, Hashes: map[string]string{"sha256": strings.Repeat("a", 64)}})
		if f.ManualReason == "" || f.URL != "" {
			t.Fatalf("unsafe filename retained %q", name)
		}
	}
}

func TestModrinthUnknownDependencyRequirementIsMalformed(t *testing.T) {
	s := service(t, func(*http.Request) (*http.Response, error) {
		return response(`{"id":"v","project_id":"p","name":"Release","files":[],"dependencies":[{"project_id":"dep"}]}`), nil
	})
	if _, e := s.Version(context.Background(), Modrinth, "p", "v"); e == nil {
		t.Fatal("missing dependency relation treated as optional")
	}
}
func TestHangarMissingRequiredFlagIsMalformed(t *testing.T) {
	var v hangarVersion
	if e := json.Unmarshal([]byte(`{"id":1,"projectId":1,"name":"1","downloads":{},"pluginDependencies":{"PAPER":[{"projectId":2,"name":"dep"}]}}`), &v); e != nil {
		t.Fatal(e)
	}
	if _, e := hangarVersionDTO(v, time.Now()); e == nil {
		t.Fatal("missing required flag treated as optional")
	}
}
