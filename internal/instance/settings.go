package instance

import (
	"strconv"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
)

const source114 = "https://feedback.minecraft.net/hc/en-us/articles/360027306551-Minecraft-Village-Pillage-1-14-0-Java"
const source118 = "https://feedback.minecraft.net/hc/en-us/articles/4415128577293-Minecraft-Java-Edition-1-18"
const source1219 = "https://www.minecraft.net/en-us/article/minecraft-java-edition-1-21-9"
const source12111 = "https://www.minecraft.net/en-us/article/minecraft-java-edition-1-21-11"
const sourcePaperOverride = "https://github.com/PaperMC/Paper/blob/ver/1.21.10/paper-server/src/main/java/org/bukkit/craftbukkit/CraftWorld.java"

type SettingCapability struct {
	Available bool   `json:"available"`
	Method    string `json:"method"`
	Reason    string `json:"reason"`
}

// DesiredRuntimeSetting describes a requirement, never an applied/effective value.
// Task10 must discover the rule, apply it to all appropriate worlds and read it
// back. Unsupported requirements block readiness instead of silently succeeding.
type DesiredRuntimeSetting struct {
	Kind           string                  `json:"kind"`
	Rule           string                  `json:"rule"`
	Value          bool                    `json:"value"`
	Status         string                  `json:"status"`
	Supported      bool                    `json:"supported"`
	Reason         string                  `json:"reason"`
	Source         string                  `json:"source"`
	Applicability  domain.VersionSelection `json:"applicability"`
	Scope          string                  `json:"scope"`
	Verification   string                  `json:"verification"`
	GameplayEffect string                  `json:"gameplay_effect"`
}
type SettingsProjection struct {
	LegacyNumericEnums bool                    `json:"legacy_numeric_enums"`
	SimulationDistance SettingCapability       `json:"simulation_distance"`
	PVP                SettingCapability       `json:"pvp"`
	DesiredRuntime     []DesiredRuntimeSetting `json:"desired_runtime"`
	SeedReason         string                  `json:"seed_reason"`
}
type settingsVersion struct{ family, minor, patch int }

func parseSettingsVersion(value string) (settingsVersion, bool) {
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return settingsVersion{}, false
	}
	v := settingsVersion{}
	targets := []*int{&v.family, &v.minor, &v.patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || strconv.Itoa(n) != p {
			return settingsVersion{}, false
		}
		*targets[i] = n
	}
	if v.family == 1 {
		if v.minor < 12 || v.minor > 21 || v.minor == 12 && v.patch < 2 {
			return v, false
		}
	} else if v.family == 26 {
		if v.minor < 1 || v.minor > 3 {
			return v, false
		}
	} else {
		return v, false
	}
	return v, true
}
func (v settingsVersion) atLeast(family, minor, patch int) bool {
	if v.family != family {
		return v.family > family
	}
	if v.minor != minor {
		return v.minor > minor
	}
	return v.patch >= patch
}
func SettingsFor(i domain.Instance, c domain.InstanceConfig) SettingsProjection {
	out := SettingsProjection{DesiredRuntime: []DesiredRuntimeSetting{}, SeedReason: "The seed is used only when generating a new world; changing it does not recreate or reset an existing world."}
	v, ok := parseSettingsVersion(i.MinecraftVersion)
	if !ok {
		reason := "Settings behavior is unavailable for this unreviewed version family."
		out.SimulationDistance = SettingCapability{false, "unsupported", reason}
		out.PVP = SettingCapability{false, "unsupported", reason}
		return out
	}
	out.LegacyNumericEnums = !v.atLeast(1, 14, 0)
	out.SimulationDistance = SettingCapability{true, "server_property", "Desired value is written to server.properties for the next start; its running value is not verified."}
	if !v.atLeast(1, 18, 0) {
		out.SimulationDistance = SettingCapability{false, "unsupported", "Independent simulation distance requires Minecraft 1.18 or later; this desired value is not written for the selected version."}
	}
	out.PVP = SettingCapability{true, "server_property", "Desired PVP is written to server.properties for the next start; its running value is not verified."}
	if !v.atLeast(1, 21, 9) {
		return out
	}
	out.PVP = SettingCapability{true, "runtime_gamerule", "Desired PVP requires a runtime gamerule change and readback after start; it is not yet verified or applied."}
	rule := "pvp"
	source := source1219
	if v.atLeast(1, 21, 11) {
		rule = "minecraft:pvp"
		source = source12111
	}
	supported := true
	reason := out.PVP.Reason
	switch i.Flavor {
	case domain.FlavorVanilla:
	case domain.FlavorPaper:
		if !v.atLeast(1, 21, 11) {
			reason += " Paper 1.21.9–1.21.10 permits per-world PVP overrides through plugins; native-rule readback does not verify those gameplay overrides."
		} else {
			reason += " Paper plugins can still affect combat; gameplay effects are not verified."
		}

	case domain.FlavorFabric, domain.FlavorForge, domain.FlavorNeoForge:
		reason += " This is the upstream Minecraft rule; installed mods may override combat and require additional verification."
	default:
		supported = false
		reason = "PVP runtime mapping is unsupported for this server software."
		out.PVP = SettingCapability{false, "unsupported", reason}
	}
	out.PVP.Reason = reason
	out.DesiredRuntime = []DesiredRuntimeSetting{{Kind: "boolean_gamerule", Rule: rule, Value: c.PVP, Status: "unverified", Supported: supported, Reason: reason, Source: source, Applicability: domain.VersionSelection{Flavor: i.Flavor, MinecraftVersion: i.MinecraftVersion, LoaderVersion: i.LoaderVersion}, Scope: "all_discovered_worlds", Verification: "native_rule_readback", GameplayEffect: "not_verified"}}
	return out
}

// SettingsCapabilities applies the same reviewed structural version/flavor
// family checks as creation. It does not claim a provider tuple is qualified.
func SettingsCapabilities(selection domain.VersionSelection) (SettingsProjection, error) {
	if _, err := managedruntime.Select(selection); err != nil {
		return SettingsProjection{}, domain.Validation([]domain.FieldError{{Field: "minecraft_version", Message: err.Error()}})
	}
	return SettingsFor(domain.Instance{Flavor: selection.Flavor, MinecraftVersion: selection.MinecraftVersion, LoaderVersion: selection.LoaderVersion}, DefaultConfig()), nil
}
