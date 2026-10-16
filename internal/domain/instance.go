// Package domain defines the portable contracts shared by Minecraft manager services.
package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Flavor string

const (
	FlavorVanilla  Flavor = "vanilla"
	FlavorPaper    Flavor = "paper"
	FlavorFabric   Flavor = "fabric"
	FlavorForge    Flavor = "forge"
	FlavorNeoForge Flavor = "neoforge"
)

type Instance struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Flavor           Flavor    `json:"flavor"`
	MinecraftVersion string    `json:"minecraft_version"`
	LoaderVersion    string    `json:"loader_version,omitempty"`
	JavaMajor        int       `json:"java_major"`
	JavaRuntimeID    string    `json:"java_runtime_id,omitempty"`
	MemoryMiB        int       `json:"memory_mib"`
	GamePort         int       `json:"game_port"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

var instanceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
var releasePattern = regexp.MustCompile(`^(1|2[6-9]|[3-9][0-9])\.(\d+)(?:\.(\d+))?$`)

func ValidateInstanceID(id string) error {
	if !instanceIDPattern.MatchString(id) {
		return fmt.Errorf("instance id must contain 1-32 lowercase letters, digits or hyphens, beginning with a letter or digit")
	}
	return nil
}

// Validate checks structural input. Provider catalogs must separately confirm that
// the exact game/loader/Java combination exists and is installable.
func (i Instance) Validate() error {
	if err := ValidateInstanceID(i.ID); err != nil {
		return err
	}
	if strings.TrimSpace(i.Name) == "" || len(i.Name) > 100 {
		return fmt.Errorf("name must contain 1-100 bytes")
	}
	switch i.Flavor {
	case FlavorVanilla, FlavorPaper, FlavorFabric, FlavorForge, FlavorNeoForge:
	default:
		return fmt.Errorf("unknown server flavor %q", i.Flavor)
	}
	m := releasePattern.FindStringSubmatch(i.MinecraftVersion)
	if m == nil {
		return fmt.Errorf("Minecraft version must be a stable release at least 1.12.2")
	}
	minor, err := strconv.Atoi(m[2])
	if err != nil {
		return fmt.Errorf("invalid Minecraft version: %w", err)
	}
	patch := 0
	if m[3] != "" {
		patch, err = strconv.Atoi(m[3])
		if err != nil {
			return fmt.Errorf("invalid Minecraft patch: %w", err)
		}
	}
	if m[1] == "1" && (minor < 12 || (minor == 12 && patch < 2)) {
		return fmt.Errorf("Minecraft versions before 1.12.2 are unsupported")
	}
	if i.JavaMajor < 8 || i.JavaMajor > 99 {
		return fmt.Errorf("Java major must be between 8 and 99")
	}
	if i.MemoryMiB < 256 || i.MemoryMiB > 1048576 {
		return fmt.Errorf("memory must be between 256 and 1048576 MiB")
	}
	if i.GamePort < 1 || i.GamePort > 65535 {
		return fmt.Errorf("game port must be between 1 and 65535")
	}
	return nil
}

// ValidateFilename is portable: Windows drive and separator syntax is rejected
// even when validating on Linux.
func ValidateFilename(name string) error {
	if name == "" || name == "." || strings.Contains(name, "..") || strings.ContainsAny(name, "/\\:\x00") {
		return fmt.Errorf("expected a plain filename")
	}
	return nil
}

type InstanceConfig struct {
	MOTD               string            `json:"motd"`
	Difficulty         string            `json:"difficulty"`
	GameMode           string            `json:"game_mode"`
	MaxPlayers         int               `json:"max_players"`
	OnlineMode         bool              `json:"online_mode"`
	PVP                bool              `json:"pvp"`
	ViewDistance       int               `json:"view_distance"`
	SimulationDistance int               `json:"simulation_distance"`
	LevelName          string            `json:"level_name"`
	LevelSeed          string            `json:"level_seed"`
	Extra              map[string]string `json:"extra"`
}

type InstanceState string

const (
	StateStopped  InstanceState = "stopped"
	StateStarting InstanceState = "starting"
	StateRunning  InstanceState = "running"
	StateStopping InstanceState = "stopping"
	StateUnknown  InstanceState = "unknown"
	StateCrashed  InstanceState = "crashed"
)
