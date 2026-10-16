package instance

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func DefaultConfig() domain.InstanceConfig {
	return domain.InstanceConfig{MOTD: "A Minecraft Server", Difficulty: "normal", GameMode: "survival", MaxPlayers: 20, OnlineMode: true, PVP: true, ViewDistance: 10, SimulationDistance: 10, LevelName: "world", Extra: map[string]string{}}
}

var ownedProperties = map[string]bool{"motd": true, "difficulty": true, "gamemode": true, "max-players": true, "online-mode": true, "pvp": true, "view-distance": true, "simulation-distance": true, "level-name": true, "level-seed": true, "server-port": true, "server-ip": true, "enable-rcon": true, "rcon.port": true, "rcon.password": true, "enable-query": true, "query.port": true, "management-server-enabled": true, "management-server-host": true, "management-server-port": true, "management-server-secret": true, "management-server-tls-enabled": true, "management-server-tls-keystore": true, "management-server-tls-keystore-password": true}

func forbiddenProperty(k string) bool {
	k = strings.ToLower(k)
	return ownedProperties[k] || strings.HasPrefix(k, "rcon.") || strings.HasPrefix(k, "management-server-") || strings.Contains(k, "password") || strings.Contains(k, "secret") || strings.Contains(k, "token")
}
func ValidateConfig(c domain.InstanceConfig) error {
	var fields []domain.FieldError
	bad := func(k, m string) { fields = append(fields, domain.FieldError{Field: k, Message: m}) }
	if len(c.MOTD) > 2048 || strings.ContainsRune(c.MOTD, 0) {
		bad("motd", "must be at most 2048 bytes without NUL")
	}
	switch c.Difficulty {
	case "peaceful", "easy", "normal", "hard":
	default:
		bad("difficulty", "choose peaceful, easy, normal or hard")
	}
	switch c.GameMode {
	case "survival", "creative", "adventure", "spectator":
	default:
		bad("game_mode", "choose survival, creative, adventure or spectator")
	}
	if c.MaxPlayers < 1 || c.MaxPlayers > 10000 {
		bad("max_players", "must be between 1 and 10000")
	}
	if c.ViewDistance < 2 || c.ViewDistance > 32 {
		bad("view_distance", "must be between 2 and 32")
	}
	if c.SimulationDistance < 2 || c.SimulationDistance > 32 {
		bad("simulation_distance", "must be between 2 and 32")
	}
	if err := portablePart(c.LevelName); err != nil || len(c.LevelName) > 64 || strings.Contains(c.LevelName, "..") {
		bad("level_name", "must be a single portable directory name, at most 64 bytes")
	}
	if len(c.LevelSeed) > 256 || strings.ContainsAny(c.LevelSeed, "\x00\r\n") {
		bad("level_seed", "must be at most 256 bytes without line breaks or NUL")
	}
	if len(c.Extra) > 200 {
		bad("extra", "at most 200 properties")
	}
	for k, v := range c.Extra {
		if k == "" || len(k) > 128 || len(v) > 8192 || strings.ContainsAny(k, "\x00\r\n") || strings.ContainsRune(v, 0) || forbiddenProperty(k) {
			bad("extra", "invalid or manager-owned property: "+safeKey(k))
		}
	}
	return domain.Validation(fields)
}
func safeKey(k string) string {
	if len(k) > 128 {
		return "(overlong key)"
	}
	return strings.ReplaceAll(strings.ReplaceAll(k, "\n", ""), "\r", "")
}

// Java .properties escapes prevent unknown keys and values injecting new entries;
// UTF-16 escapes also preserve arbitrary Unicode on legacy Java properties APIs.
func propertyEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '=', ':', '#', '!', ' ':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			if r < 32 || r > 126 {
				for _, u := range utf16.Encode([]rune{r}) {
					fmt.Fprintf(&b, `\u%04x`, u)
				}
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
func Properties(i domain.Instance, c domain.InstanceConfig) []byte {
	values := map[string]string{}
	for k, v := range c.Extra {
		values[k] = v
	}
	for k, v := range map[string]string{"motd": c.MOTD, "difficulty": c.Difficulty, "gamemode": c.GameMode, "max-players": strconv.Itoa(c.MaxPlayers), "online-mode": strconv.FormatBool(c.OnlineMode), "pvp": strconv.FormatBool(c.PVP), "view-distance": strconv.Itoa(c.ViewDistance), "simulation-distance": strconv.Itoa(c.SimulationDistance), "level-name": c.LevelName, "level-seed": c.LevelSeed, "server-port": strconv.Itoa(i.GamePort), "server-ip": "", "enable-rcon": "false", "enable-query": "false", "management-server-enabled": "false"} {
		values[k] = v
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Managed by Minecraft Server UI\n")
	for _, k := range keys {
		b.WriteString(propertyEscape(k))
		b.WriteByte('=')
		b.WriteString(propertyEscape(values[k]))
		b.WriteByte('\n')
	}
	return []byte(b.String())
}
