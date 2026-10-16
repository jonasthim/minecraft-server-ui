// Package paper resolves exact stable builds through the official Fill v3 API.
// VersionSelection.LoaderVersion is the numeric Paper build ID, not a loader.
package paper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
	"github.com/jonasthim/minecraft-server-ui/internal/server/providerutil"
)

const projectURL = "https://fill.papermc.io/v3/projects/paper"

var buildID = regexp.MustCompile(`^[1-9][0-9]{0,9}$`)

type Provider struct {
	client    *providerutil.Client
	installer *providerutil.Installer
}

var _ domain.DistributionProvider = (*Provider)(nil)

func New(dataDir string, client *http.Client) *Provider {
	c := providerutil.NewClient(client, providerutil.Paper)
	return &Provider{client: c, installer: providerutil.NewInstaller(dataDir, c)}
}
func (*Provider) Flavor() domain.Flavor { return domain.FlavorPaper }
func (p *Provider) versions(ctx context.Context) ([]string, providerutil.Source, error) {
	var m struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
		Versions map[string][]string `json:"versions"`
	}
	source, err := p.client.Metadata(ctx, projectURL, &m)
	if err != nil {
		return nil, source, err
	}
	if m.Project.ID != "paper" || len(m.Versions) == 0 {
		return nil, source, errors.New("invalid Paper project catalog")
	}
	versions := []string{}
	seen := map[string]bool{}
	for _, group := range m.Versions {
		for _, v := range group {
			if _, err = managedruntime.Select(domain.VersionSelection{Flavor: domain.FlavorPaper, MinecraftVersion: v}); err != nil {
				continue
			}
			if seen[v] {
				return nil, source, errors.New("duplicate Paper release")
			}
			seen[v] = true
			versions = append(versions, v)
		}
	}
	providerutil.SortVersions(versions)
	return versions, source, nil
}

// Versions lists eligible published Minecraft versions; Builds determines which
// have stable downloadable artifacts. It never invents historical availability.
func (p *Provider) Versions(ctx context.Context) ([]string, error) {
	versions, _, err := p.versions(ctx)
	return versions, err
}

type build struct {
	ID        int    `json:"id"`
	Channel   string `json:"channel"`
	Downloads map[string]struct {
		Name      string `json:"name"`
		Size      int64  `json:"size"`
		URL       string `json:"url"`
		Checksums struct {
			SHA256 string `json:"sha256"`
		} `json:"checksums"`
	} `json:"downloads"`
}

func (b build) artifact(version string, source providerutil.Source) (domain.Artifact, error) {
	d, ok := b.Downloads["server:default"]
	if !ok || b.ID <= 0 || b.ID > 2147483647 || d.Name != fmt.Sprintf("paper-%s-%d.jar", version, b.ID) {
		return domain.Artifact{}, errors.New("invalid Paper build artifact")
	}
	a := domain.Artifact{ID: fmt.Sprintf("paper/%s/%d", version, b.ID), Provider: "paper", ProjectID: "paper", VersionID: fmt.Sprintf("%s/%d", version, b.ID), Filename: d.Name, SourceURL: d.URL, Size: d.Size, Checksum: domain.Checksum{Algorithm: "sha256", Value: d.Checksums.SHA256}, RetrievedAt: source.RetrievedAt}
	if err := providerutil.CheckArtifact(providerutil.Paper, a); err != nil {
		return domain.Artifact{}, err
	}
	return a, nil
}
func (p *Provider) builds(ctx context.Context, version string) ([]build, []providerutil.Source, error) {
	if _, err := managedruntime.Select(domain.VersionSelection{Flavor: domain.FlavorPaper, MinecraftVersion: version}); err != nil {
		return nil, nil, err
	}
	versions, indexSource, err := p.versions(ctx)
	if err != nil {
		return nil, nil, err
	}
	found := false
	for _, v := range versions {
		if v == version {
			found = true
			break
		}
	}
	if !found {
		return nil, nil, errors.New("Minecraft release is unavailable in the official Paper catalog")
	}
	var response []build
	source, err := p.client.Metadata(ctx, projectURL+"/versions/"+version+"/builds", &response)
	if err != nil {
		return nil, nil, err
	}
	stable := []build{}
	seen := map[int]bool{}
	for _, b := range response {
		if b.Channel != "STABLE" {
			continue
		}
		if seen[b.ID] {
			return nil, nil, errors.New("duplicate Paper build")
		}
		seen[b.ID] = true
		if _, err = b.artifact(version, source); err != nil {
			return nil, nil, err
		}
		stable = append(stable, b)
	}
	sort.Slice(stable, func(i, j int) bool { return stable[i].ID > stable[j].ID })
	return stable, []providerutil.Source{indexSource, source}, nil
}

// Builds returns exact STABLE numeric build IDs in descending order. Callers may
// select an ID and persist it as LoaderVersion; Resolve rejects floating labels.
func (p *Provider) Builds(ctx context.Context, version string) ([]string, error) {
	builds, _, err := p.builds(ctx, version)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(builds))
	for _, b := range builds {
		ids = append(ids, strconv.Itoa(b.ID))
	}
	return ids, nil
}
func (p *Provider) resolve(ctx context.Context, s domain.VersionSelection) (domain.InstallPlan, []providerutil.Source, error) {
	fail := func(e error) (domain.InstallPlan, []providerutil.Source, error) { return domain.InstallPlan{}, nil, e }
	if s.Flavor != domain.FlavorPaper || !buildID.MatchString(s.LoaderVersion) {
		return fail(errors.New("Paper requires an exact numeric build ID"))
	}
	requirements, err := managedruntime.Select(s)
	if err != nil {
		return fail(err)
	}
	builds, sources, err := p.builds(ctx, s.MinecraftVersion)
	if err != nil {
		return fail(err)
	}
	for _, b := range builds {
		if strconv.Itoa(b.ID) != s.LoaderVersion {
			continue
		}
		a, e := b.artifact(s.MinecraftVersion, sources[1])
		if e != nil {
			return fail(e)
		}
		plan := domain.InstallPlan{Selection: s, Requirements: requirements, Artifacts: []domain.Artifact{a}, Capabilities: providerutil.Capabilities(s), Warnings: []string{"Publisher metadata does not establish tested server/client support."}}
		return plan, sources, nil
	}
	return fail(errors.New("requested stable Paper build is unavailable"))
}
func (p *Provider) Resolve(ctx context.Context, s domain.VersionSelection) (domain.InstallPlan, error) {
	plan, _, err := p.resolve(ctx, s)
	return plan, err
}

// Install accepts an instance ID and publishes immutable executable files only.
// Returning LaunchSpec does not start Paper, accept the EULA or persist activation.
func (p *Provider) Install(ctx context.Context, instanceID string, plan domain.InstallPlan, r domain.JavaRuntime, w io.Writer) (domain.LaunchSpec, error) {
	canonical, sources, err := p.resolve(ctx, plan.Selection)
	if err != nil {
		return domain.LaunchSpec{}, err
	}
	if !providerutil.SamePlan(plan, canonical) {
		return domain.LaunchSpec{}, errors.New("installation plan differs from official pinned artifacts")
	}
	return p.installer.Publish(ctx, instanceID, canonical, r, sources, w)
}
