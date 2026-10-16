// Package fabric installs exact Fabric Meta tuples without running downloaded scripts.
package fabric

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
	"github.com/jonasthim/minecraft-server-ui/internal/server/loaderutil"
	"github.com/jonasthim/minecraft-server-ui/internal/server/providerutil"
	"github.com/jonasthim/minecraft-server-ui/internal/server/vanilla"
)

const meta = "https://meta.fabricmc.net/v2/versions"

type Provider struct {
	client    *loaderutil.Client
	publisher *loaderutil.Publisher
	vanilla   *vanilla.Provider
}

var _ domain.DistributionProvider = (*Provider)(nil)

func New(dir string, c *http.Client) *Provider {
	client := loaderutil.NewClient(c)
	return &Provider{client, loaderutil.NewPublisher(dir, client), vanilla.New(dir, c)}
}
func (*Provider) Flavor() domain.Flavor { return domain.FlavorFabric }

type game struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
}
type loader struct {
	Version string `json:"version"`
	Maven   string `json:"maven"`
}
type tuple struct {
	Loader       loader `json:"loader"`
	Intermediary loader `json:"intermediary"`
	Launcher     struct {
		Version   int `json:"version"`
		MinJava   int `json:"min_java_version"`
		MainClass struct {
			Server string `json:"server"`
		} `json:"mainClass"`
		Libraries struct {
			Common []struct {
				Name string `json:"name"`
				URL  string `json:"url"`
			} `json:"common"`
			Server []struct {
				Name string `json:"name"`
				URL  string `json:"url"`
			} `json:"server"`
		} `json:"libraries"`
	} `json:"launcherMeta"`
}

func (p *Provider) Versions(ctx context.Context) ([]string, error) {
	var games []game
	if _, e := p.client.JSON(ctx, meta+"/game", &games); e != nil {
		return nil, e
	}
	out := []string{}
	seen := map[string]bool{}
	for _, g := range games {
		if !g.Stable {
			continue
		}
		if _, e := managedruntime.Select(domain.VersionSelection{Flavor: domain.FlavorFabric, MinecraftVersion: g.Version}); e != nil {
			continue
		}
		if seen[g.Version] {
			return nil, errors.New("duplicate Fabric game version")
		}
		seen[g.Version] = true
		out = append(out, g.Version)
	}
	providerutil.SortVersions(out)
	return out, nil
}
func (p *Provider) Loaders(ctx context.Context, mc string) ([]string, error) {
	if _, e := managedruntime.Select(domain.VersionSelection{Flavor: domain.FlavorFabric, MinecraftVersion: mc}); e != nil {
		return nil, e
	}
	games, e := p.Versions(ctx)
	if e != nil {
		return nil, e
	}
	found := false
	for _, g := range games {
		if g == mc {
			found = true
		}
	}
	if !found {
		return nil, errors.New("game is unavailable in Fabric Meta")
	}
	var tuples []struct {
		Loader       loader `json:"loader"`
		Intermediary loader `json:"intermediary"`
	}
	if _, e = p.client.JSON(ctx, meta+"/loader/"+mc, &tuples); e != nil {
		return nil, e
	}
	out := []string{}
	seen := map[string]bool{}
	for _, t := range tuples {
		if !loaderutil.ExactVersion(t.Loader.Version) || t.Loader.Maven != "net.fabricmc:fabric-loader:"+t.Loader.Version || t.Intermediary.Version != mc || t.Intermediary.Maven != "net.fabricmc:intermediary:"+mc || seen[t.Loader.Version] {
			return nil, errors.New("malformed Fabric loader catalog")
		}
		seen[t.Loader.Version] = true
		out = append(out, t.Loader.Version)
	}
	return out, nil
}
func (p *Provider) resolve(ctx context.Context, s domain.VersionSelection) (domain.InstallPlan, []loaderutil.Source, tuple, error) {
	fail := func(e error) (domain.InstallPlan, []loaderutil.Source, tuple, error) {
		return domain.InstallPlan{}, nil, tuple{}, e
	}
	if s.Flavor != domain.FlavorFabric || !loaderutil.ExactVersion(s.LoaderVersion) {
		return fail(errors.New("Fabric requires an exact loader version"))
	}
	req, e := managedruntime.Select(s)
	if e != nil {
		return fail(e)
	}
	loaders, e := p.Loaders(ctx, s.MinecraftVersion)
	if e != nil {
		return fail(e)
	}
	found := false
	for _, v := range loaders {
		if v == s.LoaderVersion {
			found = true
		}
	}
	if !found {
		return fail(errors.New("exact Fabric loader tuple unavailable"))
	}
	var t tuple
	source, e := p.client.JSON(ctx, meta+"/loader/"+s.MinecraftVersion+"/"+s.LoaderVersion, &t)
	if e != nil {
		return fail(e)
	}
	if t.Loader.Version != s.LoaderVersion || t.Loader.Maven != "net.fabricmc:fabric-loader:"+s.LoaderVersion || t.Intermediary.Version != s.MinecraftVersion || t.Intermediary.Maven != "net.fabricmc:intermediary:"+s.MinecraftVersion || t.Launcher.Version < 1 || t.Launcher.Version > 2 || t.Launcher.MinJava > req.JavaMajor {
		return fail(errors.New("Fabric tuple metadata or Java floor differs from reviewed requirements"))
	}
	main := t.Launcher.MainClass.Server
	if main != "net.fabricmc.loader.impl.launch.knot.KnotServer" && main != "net.fabricmc.loader.launch.knot.KnotServer" {
		return fail(errors.New("unreviewed Fabric server main class"))
	}
	v, e := p.vanilla.Resolve(ctx, domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: s.MinecraftVersion})
	if e != nil {
		return fail(e)
	}
	artifacts := append([]domain.Artifact{}, v.Artifacts...)
	sources := []loaderutil.Source{source}
	coordinates := []string{t.Loader.Maven, t.Intermediary.Maven}
	for _, lib := range append(t.Launcher.Libraries.Common, t.Launcher.Libraries.Server...) {
		if lib.URL != "https://maven.fabricmc.net/" {
			return fail(errors.New("unreviewed Fabric library publisher"))
		}
		coordinates = append(coordinates, lib.Name)
	}
	if len(coordinates) > 256 {
		return fail(errors.New("too many Fabric libraries"))
	}
	seen := map[string]bool{}
	for _, coordinate := range coordinates {
		if seen[coordinate] {
			return fail(errors.New("duplicate Fabric library"))
		}
		seen[coordinate] = true
		a, src, e := p.client.Artifact(ctx, "fabric", coordinate, "https://maven.fabricmc.net/")
		if e != nil {
			return fail(e)
		}
		artifacts = append(artifacts, a)
		sources = append(sources, src...)
	}
	cap := providerutil.Capabilities(s)
	cap.Mods = domain.Availability{Available: true}
	cap.Plugins = domain.Availability{Reason: "Fabric does not natively load server plugins"}
	return domain.InstallPlan{Selection: s, Requirements: req, Artifacts: artifacts, Capabilities: cap, Warnings: []string{"Exact published metadata is not tested server/client qualification."}}, sources, t, nil
}
func (p *Provider) Resolve(ctx context.Context, s domain.VersionSelection) (domain.InstallPlan, error) {
	plan, _, _, e := p.resolve(ctx, s)
	return plan, e
}
func (p *Provider) Install(ctx context.Context, id string, plan domain.InstallPlan, r domain.JavaRuntime, w io.Writer) (domain.LaunchSpec, error) {
	canonical, sources, t, e := p.resolve(ctx, plan.Selection)
	if e != nil {
		return domain.LaunchSpec{}, e
	}
	if !providerutil.SamePlan(plan, canonical) {
		return domain.LaunchSpec{}, errors.New("Fabric plan differs from exact official artifacts")
	}
	build := func(ctx context.Context, root *os.Root, stage, final string, reuse bool) ([]string, error) {
		if !reuse {
			for n, a := range canonical.Artifacts {
				name := "server.jar"
				if n > 0 {
					path, e := loaderutil.MavenPath(a.ID)
					if e != nil {
						return nil, e
					}
					name = "libraries/" + path
				}
				if e := loaderutil.DownloadFile(ctx, p.client, root, name, a); e != nil {
					return nil, e
				}
			}
		}
		return loaderutil.FabricLaunch(ctx, root, canonical, final, t.Launcher.MainClass.Server, reuse)
	}
	return p.publisher.Publish(ctx, id, canonical, r, sources, w, build)
}

func (*Provider) ManualDownloadURL() string { return "https://fabricmc.net/use/server/" }
