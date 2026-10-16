package runtime

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// These rules choose a conservative runtime, not a claim that a loader build or
// game tuple has passed product qualification. Providers must still resolve an
// exact published build and apply its potentially stricter requirements.
// Sources reviewed 2026-10-16:
// https://docs.minecraftforge.net/en/fg-5.x/gettingstarted/
// https://www.minecraft.net/en-us/article/minecraft-java-edition-1-20-5
// https://www.minecraft.net/en-us/article/minecraft-java-edition-26-1
// https://docs.papermc.io/paper/getting-started/
// https://wiki.fabricmc.net/install
// https://docs.minecraftforge.net/en/1.16.x/gettingstarted/
// https://docs.neoforged.net/docs/1.20.4/gettingstarted/
var minecraftVersionPattern = regexp.MustCompile(`^(1|26)\.([0-9]{1,2})(?:\.([0-9]{1,2}))?$`)

func Select(selection domain.VersionSelection) (domain.LaunchRequirements, error) {
	m := minecraftVersionPattern.FindStringSubmatch(selection.MinecraftVersion)
	if m == nil {
		return domain.LaunchRequirements{}, fmt.Errorf("unknown stable Minecraft release family")
	}
	family, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch := 0
	if m[3] != "" {
		patch, _ = strconv.Atoi(m[3])
	}
	if (family == 1 && (minor < 12 || minor > 21 || minor == 12 && patch < 2)) || (family == 26 && (minor < 1 || minor > 3)) {
		return domain.LaunchRequirements{}, fmt.Errorf("Minecraft release is outside reviewed compatibility rules")
	}
	switch selection.Flavor {
	case domain.FlavorVanilla, domain.FlavorPaper, domain.FlavorFabric, domain.FlavorForge, domain.FlavorNeoForge:
	default:
		return domain.LaunchRequirements{}, fmt.Errorf("unknown server flavor")
	}
	if selection.Flavor == domain.FlavorFabric && family == 1 && minor < 14 {
		return domain.LaunchRequirements{}, fmt.Errorf("Fabric requires Minecraft 1.14 or later")
	}
	if selection.Flavor == domain.FlavorNeoForge && family == 1 && (minor < 20 || minor == 20 && patch < 2) {
		return domain.LaunchRequirements{}, fmt.Errorf("NeoForge requires Minecraft 1.20.2 or later")
	}
	java := 8
	if family == 26 {
		java = 25
	} else if minor > 20 || minor == 20 && patch >= 5 {
		java = 21
	} else if minor >= 18 {
		java = 17
	} else if minor == 17 {
		java = 16
	}
	if selection.Flavor == domain.FlavorPaper && family == 1 {
		switch {
		case minor <= 16:
			java = 11
			if minor == 16 && patch >= 5 {
				java = 16
			}
		case minor <= 19:
			java = 17
		default:
			java = 21
		}
	}
	if selection.Flavor == domain.FlavorFabric && family == 1 && minor == 17 {
		java = 17
	}
	return domain.LaunchRequirements{JavaMajor: java, MinJavaMajor: java, MaxJavaMajor: java, RequiresInstaller: selection.Flavor == domain.FlavorForge || selection.Flavor == domain.FlavorNeoForge}, nil
}
