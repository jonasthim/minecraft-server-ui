package catalog

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func TestCurseForgeExactFlowsEnforceMinecraftContentBoundary(t *testing.T) {
	for _, operation := range []string{"versions", "version", "download"} {
		for _, tc := range []struct {
			name                         string
			projectGame, class, fileGame int
			valid                        bool
		}{{"mod", 432, 6, 432, true}, {"pack", 432, 4471, 432, true}, {"foreign project game", 3, 6, 432, false}, {"unapproved Minecraft class", 432, 12, 432, false}, {"foreign file game", 432, 6, 3, false}, {"missing file game", 432, 6, 0, false}} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				cdnCalls := 0
				s := service(t, func(r *http.Request) (*http.Response, error) {
					if r.URL.Host == "edge.forgecdn.net" {
						cdnCalls++
						return response("abc"), nil
					}
					switch r.URL.Path {
					case "/v1/mods/42":
						return response(fmt.Sprintf(`{"data":{"id":42,"gameId":%d,"classId":%d,"name":"Fixture","slug":"fixture"}}`, tc.projectGame, tc.class)), nil
					case "/v1/mods/42/files", "/v1/mods/42/files/88":
						f := fmt.Sprintf(`{"id":88,"modId":42,"gameId":%d,"displayName":"Release","fileName":"fixture.jar","fileLength":3,"isAvailable":true,"downloadUrl":"https://edge.forgecdn.net/files/fixture.jar","hashes":[{"algo":1,"value":"a9993e364706816aba3e25717850c26c9cd0d89d"}]}`, tc.fileGame)
						if strings.HasSuffix(r.URL.Path, "/files") {
							return response(`{"data":[` + f + `],"pagination":{"totalCount":1}}`), nil
						}
						return response(`{"data":` + f + `}`), nil
					}
					t.Fatalf("unexpected publisher request %s", r.URL)
					return nil, nil
				})
				ctx := context.Background()
				if _, e := s.ConfigureCurseForge(ctx, domain.User{Role: domain.RoleAdmin}, "fixture-private"); e != nil {
					t.Fatal(e)
				}
				var e error
				switch operation {
				case "versions":
					_, e = s.Versions(ctx, CurseForge, "42", Query{Limit: 1})
				case "version":
					_, e = s.Version(ctx, CurseForge, "42", "88")
				case "download":
					_, e = s.Download(ctx, Selection{Provider: CurseForge, ProjectID: "42", VersionID: "88", FileID: "88"})
				}
				if (e == nil) != tc.valid {
					t.Fatalf("valid=%v error=%v", tc.valid, e)
				}
				if !tc.valid && cdnCalls != 0 {
					t.Fatal("unapproved content reached CDN")
				}
			})
		}
	}
}
func TestModrinthSearchAndProjectVersionFieldsStayDistinct(t *testing.T) {
	s := service(t, func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			return response(`{"hits":[{"project_id":"p123","slug":"example","project_type":"mod","title":"Example","versions":["1.21.1"]}],"total_hits":1}`), nil
		}
		return response(`{"id":"p123","slug":"example","project_type":"mod","title":"Example","versions":["releaseId123"]}`), nil
	})
	r, e := s.Search(context.Background(), Query{Provider: Modrinth, Kind: Mod})
	if e != nil || strings.Join(r.Projects[0].Compatibility.Minecraft, ",") != "1.21.1" {
		t.Fatal(r, e)
	}
	p, e := s.Project(context.Background(), Modrinth, "p123")
	if e != nil || len(p.Compatibility.Minecraft) != 0 {
		t.Fatalf("project release IDs became Minecraft versions: %+v %v", p, e)
	}
}
func TestModrinthNullableAndPunctuationSlugsDoNotDropSearchPage(t *testing.T) {
	for _, slug := range []string{`null`, `""`, `"some.mod!@$()` + "`" + `.+,\"'-"`} {
		s := service(t, func(*http.Request) (*http.Response, error) {
			return response(`{"hits":[{"project_id":"p123","slug":` + slug + `,"project_type":"mod","title":"Example","versions":["1.21.1"]}],"total_hits":1}`), nil
		})
		r, e := s.Search(context.Background(), Query{Provider: Modrinth, Kind: Mod})
		if e != nil || len(r.Projects) != 1 || r.Projects[0].OfficialURL != "https://modrinth.com/mod/p123" {
			t.Fatalf("slug %s: %+v %v", slug, r, e)
		}
	}
}
func TestHangarDependencyFallbackRebindsToCanonicalRelease(t *testing.T) {
	v := Version{ID: "1.2+3", OfficialURL: "https://hangar.papermc.io/31/versions/1.2+3", Files: []File{{OfficialURL: "https://hangar.papermc.io/31/versions/1.2+3"}}, Dependencies: []Dependency{{Required: true, ManualURL: "https://hangar.papermc.io/31/versions/1.2+3"}, {Required: true, ManualURL: "https://publisher.example/dependency"}}}
	bindHangarURL(&v, Project{OfficialURL: "https://hangar.papermc.io/Author/Plugin"})
	if v.Dependencies[0].ManualURL != "https://hangar.papermc.io/Author/Plugin/versions/1.2+3" || v.Dependencies[1].ManualURL != "https://publisher.example/dependency" {
		t.Fatal(v.Dependencies)
	}
}
