// Package vanilla resolves stable Minecraft Java servers from Mojang's official
// manifest and publishes verified executable artifacts without starting the game.
package vanilla

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
	"github.com/jonasthim/minecraft-server-ui/internal/server/providerutil"
)

const manifestURL = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

type Provider struct {
	client    *providerutil.Client
	installer *providerutil.Installer
}

var _ domain.DistributionProvider = (*Provider)(nil)

func New(dataDir string, client *http.Client) *Provider {
	c := providerutil.NewClient(client, providerutil.Mojang)
	return &Provider{client: c, installer: providerutil.NewInstaller(dataDir, c)}
}
func (*Provider) Flavor() domain.Flavor { return domain.FlavorVanilla }

type release struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	URL  string `json:"url"`
	SHA1 string `json:"sha1"`
}
type catalog struct {
	Versions []release `json:"versions"`
}

func (p *Provider) catalog(ctx context.Context) ([]release, providerutil.Source, error) {
	var m catalog
	s, err := p.client.Metadata(ctx, manifestURL, &m)
	if err != nil {
		return nil, s, err
	}
	if len(m.Versions) == 0 {
		return nil, s, errors.New("Mojang catalog has no releases")
	}
	var versions []release
	seen := map[string]bool{}
	for _, v := range m.Versions {
		if v.Type != "release" {
			continue
		}
		if _, err = managedruntime.Select(domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: v.ID}); err != nil {
			continue
		}
		if seen[v.ID] {
			return nil, s, errors.New("duplicate Mojang release")
		}
		seen[v.ID] = true
		versions = append(versions, v)
	}
	return versions, s, nil
}

// Versions returns published stable releases within reviewed runtime rules.
// Catalog presence is not evidence of tested gameplay support.
func (p *Provider) Versions(ctx context.Context) ([]string, error) {
	r, _, err := p.catalog(ctx)
	if err != nil {
		return nil, err
	}
	v := make([]string, 0, len(r))
	for _, x := range r {
		v = append(v, x.ID)
	}
	providerutil.SortVersions(v)
	return v, nil
}
func (p *Provider) resolve(ctx context.Context, s domain.VersionSelection) (domain.InstallPlan, []providerutil.Source, error) {
	fail := func(e error) (domain.InstallPlan, []providerutil.Source, error) { return domain.InstallPlan{}, nil, e }
	if s.Flavor != domain.FlavorVanilla || s.LoaderVersion != "" {
		return fail(errors.New("Vanilla requires its own flavor and no loader version"))
	}
	requirements, err := managedruntime.Select(s)
	if err != nil {
		return fail(err)
	}
	versions, indexSource, err := p.catalog(ctx)
	if err != nil {
		return fail(err)
	}
	var chosen *release
	for n := range versions {
		if versions[n].ID == s.MinecraftVersion {
			chosen = &versions[n]
			break
		}
	}
	if chosen == nil {
		return fail(errors.New("Minecraft release is unavailable in the official catalog"))
	}
	if !providerutil.ValidDigest("sha1", chosen.SHA1) || !strings.HasSuffix(chosen.URL, "/"+chosen.SHA1+"/"+s.MinecraftVersion+".json") {
		return fail(errors.New("invalid Mojang version metadata reference"))
	}
	data, versionSource, err := p.client.MetadataBytes(ctx, chosen.URL)
	if err != nil {
		return fail(err)
	}
	sum := sha1.Sum(data)
	if hex.EncodeToString(sum[:]) != chosen.SHA1 {
		return fail(errors.New("Mojang version metadata checksum mismatch"))
	}
	versionSource.PublishedChecksum = &domain.Checksum{Algorithm: "sha1", Value: chosen.SHA1}
	var m struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		JavaVersion struct {
			Major int `json:"majorVersion"`
		} `json:"javaVersion"`
		Downloads struct {
			Server struct {
				SHA1 string `json:"sha1"`
				Size int64  `json:"size"`
				URL  string `json:"url"`
			} `json:"server"`
		} `json:"downloads"`
	}
	if json.Unmarshal(data, &m) != nil {
		return fail(errors.New("invalid Mojang version metadata JSON"))
	}
	if m.ID != s.MinecraftVersion || m.Type != "release" || m.JavaVersion.Major != requirements.JavaMajor {
		return fail(errors.New("Mojang version identity or Java requirement differs from reviewed tuple"))
	}
	a := domain.Artifact{ID: "vanilla/" + m.ID, Provider: "mojang", ProjectID: "minecraft-java", VersionID: m.ID, Filename: "minecraft_server." + m.ID + ".jar", Size: m.Downloads.Server.Size, SourceURL: m.Downloads.Server.URL, Checksum: domain.Checksum{Algorithm: "sha1", Value: m.Downloads.Server.SHA1}, RetrievedAt: versionSource.RetrievedAt}
	if err = providerutil.CheckArtifact(providerutil.Mojang, a); err != nil {
		return fail(err)
	}
	plan := domain.InstallPlan{Selection: s, Requirements: requirements, Artifacts: []domain.Artifact{a}, Capabilities: providerutil.Capabilities(s), Warnings: []string{"Mojang publishes SHA1 for this artifact; installation also records a local SHA256.", "Publisher metadata does not establish tested server/client support."}}
	return plan, []providerutil.Source{indexSource, versionSource}, nil
}
func (p *Provider) Resolve(ctx context.Context, s domain.VersionSelection) (domain.InstallPlan, error) {
	plan, _, err := p.resolve(ctx, s)
	return plan, err
}

// Install validates instanceID through the canonical instance path helpers,
// re-resolves publisher metadata, and revalidates the exact managed runtime.
// It returns an argv proposal; the lifecycle coordinator owns activation and EULA.
func (p *Provider) Install(ctx context.Context, instanceID string, plan domain.InstallPlan, r domain.JavaRuntime, w io.Writer) (domain.LaunchSpec, error) {
	canonical, sources, err := p.resolve(ctx, plan.Selection)
	if err != nil {
		return domain.LaunchSpec{}, err
	}
	if !providerutil.SamePlan(plan, canonical) {
		return domain.LaunchSpec{}, fmt.Errorf("installation plan differs from official pinned artifacts")
	}
	return p.installer.Publish(ctx, instanceID, canonical, r, sources, w)
}
