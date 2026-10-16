package instance

import (
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"strings"
	"testing"
)

// Independently verified against official 1.12.2 server bytecode enum constructors:
// tz: peaceful=0,easy=1,normal=2,hard=3; ams: survival=0,creative=1,adventure=2,spectator=3.
func TestLegacyPropertiesUseVerifiedNumericValues(t *testing.T) {
	for _, version := range []string{"1.12.2", "1.13.2"} {
		for _, pair := range []struct{ difficulty, mode, number string }{{"peaceful", "survival", "0"}, {"easy", "creative", "1"}, {"normal", "adventure", "2"}, {"hard", "spectator", "3"}} {
			d := draft("alpha", 25565)
			d.Instance.MinecraftVersion = version
			d.Config.Difficulty = pair.difficulty
			d.Config.GameMode = pair.mode
			raw := string(Properties(d.Instance, d.Config))
			if !strings.Contains(raw, "difficulty="+pair.number+"\n") || !strings.Contains(raw, "gamemode="+pair.number+"\n") {
				t.Fatalf("%s: %s", version, raw)
			}
		}
	}
}
func TestVersionedPropertyAvailability(t *testing.T) {
	for _, version := range []string{"1.14", "1.17.1", "1.18", "1.21.8", "1.21.9", "1.21.11", "26.1", "26.3"} {
		d := draft("alpha", 25565)
		d.Instance.MinecraftVersion = version
		raw := string(Properties(d.Instance, d.Config))
		if !strings.Contains(raw, "difficulty=normal\n") || !strings.Contains(raw, "gamemode=survival\n") {
			t.Fatal(version, raw)
		}
		simulation := version != "1.14" && version != "1.17.1"
		pvpProperty := version == "1.14" || version == "1.17.1" || version == "1.18" || version == "1.21.8"
		if strings.Contains(raw, "simulation-distance=") != simulation || strings.Contains(raw, "pvp=") != pvpProperty {
			t.Fatal(version, raw)
		}
	}
}
func TestRuntimePVPUsesExactVerifiedRulesAndNoAppliedClaim(t *testing.T) {
	for _, tc := range []struct{ version, rule string }{{"1.21.9", "pvp"}, {"1.21.10", "pvp"}, {"1.21.11", "minecraft:pvp"}, {"26.1", "minecraft:pvp"}, {"26.3", "minecraft:pvp"}} {
		d := draft("alpha", 25565)
		d.Instance.MinecraftVersion = tc.version
		settings := SettingsFor(d.Instance, d.Config)
		if len(settings.DesiredRuntime) != 1 || settings.DesiredRuntime[0].Rule != tc.rule || settings.DesiredRuntime[0].Status != "unverified" {
			t.Fatal(tc, settings)
		}
		if settings.PVP.Method != "runtime_gamerule" {
			t.Fatal(settings)
		}
	}
	d := draft("alpha", 25565)
	d.Instance.Flavor = domain.FlavorPaper
	d.Instance.MinecraftVersion = "1.21.10"
	settings := SettingsFor(d.Instance, d.Config)
	if !strings.Contains(settings.PVP.Reason, "override") || !settings.PVP.Available || !settings.DesiredRuntime[0].Supported {
		t.Fatal("Paper world override caveat absent", settings)
	}
}
