package providerutil

import (
	"sort"
	"strconv"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// SortVersions orders numeric stable versions newest first without lexical traps.
func SortVersions(v []string) {
	sort.Slice(v, func(i, j int) bool {
		a, b := strings.Split(v[i], "."), strings.Split(v[j], ".")
		for n := 0; n < 3; n++ {
			x, y := 0, 0
			if n < len(a) {
				x, _ = strconv.Atoi(a[n])
			}
			if n < len(b) {
				y, _ = strconv.Atoi(b[n])
			}
			if x != y {
				return x > y
			}
		}
		return v[i] > v[j]
	})
}

// Capabilities describe native distribution facilities, not product qualification.
func Capabilities(s domain.VersionSelection) domain.Capabilities {
	parts := strings.Split(s.MinecraftVersion, ".")
	family, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	datapacks := family >= 26 || minor >= 13
	c := domain.Capabilities{Console: domain.Availability{Available: true}, RCON: domain.Availability{Available: true}, Plugins: domain.Availability{Available: s.Flavor == domain.FlavorPaper}, Datapacks: domain.Availability{Available: datapacks}, LiveBackup: domain.Availability{Available: true}, Mods: domain.Availability{Reason: "This distribution does not provide a mod loader"}, Map: domain.Availability{Reason: "World decoder qualification is managed separately"}, Metrics: domain.Availability{Reason: "JVM metrics collection is managed separately"}}
	if !c.Plugins.Available {
		c.Plugins.Reason = "Vanilla does not load server plugins"
	}
	if !datapacks {
		c.Datapacks.Reason = "Datapacks require Minecraft 1.13 or later"
	}
	return c
}
