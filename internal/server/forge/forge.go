// Package forge installs exact official Forge tuples into immutable releases.
package forge

import (
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/server/loaderutil"
	"net/http"
)

type Provider struct{ *loaderutil.InstallerProvider }

var _ domain.DistributionProvider = (*Provider)(nil)

func New(dataDir string, client *http.Client) *Provider {
	return &Provider{loaderutil.NewInstallerProvider(dataDir, client, domain.FlavorForge)}
}
