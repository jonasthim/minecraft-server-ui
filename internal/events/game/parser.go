// Package game derives partial observations from the existing redacted console
// history. It does not read game files or establish authenticated player identity.
package game

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jonasthim/minecraft-server-ui/internal/console"
)

const parserVersion = "native-en-1.12.2-1.21.9-1.21.11-26.3-v1"

var nativeLine = regexp.MustCompile(`^\[(?:[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]\] \[Server thread/INFO\]: (?:System chat: )?([A-Za-z0-9_]{1,16}) (.+)$`)
var advancement = regexp.MustCompile(`^has (?:made the advancement|completed the challenge|reached the goal) \[([^\[\]]{1,256})\]$`)

// These are bounded, exact translated templates from the official bundled
// en_us resources. The explicit subset deliberately leaves other deaths unknown.
var simpleDeaths = map[string]bool{
	"was struck by lightning": true, "went up in flames": true, "burned to death": true,
	"tried to swim in lava": true, "discovered floor was lava": true, "discovered the floor was lava": true,
	"suffocated in a wall": true, "was squished too much": true, "drowned": true,
	"starved to death": true, "was pricked to death": true, "died": true, "blew up": true,
	"was killed by magic": true, "withered away": true, "was squashed by a falling anvil": true,
	"was squashed by a falling block": true, "hit the ground too hard": true, "fell out of the world": true,
	"was roasted in dragon breath": true, "was roasted in dragon's breath": true, "experienced kinetic energy": true,
	"went off with a bang": true,
}
var attributedDeath = regexp.MustCompile(`^(?:was slain by|was shot by|was fireballed by|was killed by|was blown up by) [^\[\]<>]{1,256}$`)

func parse(line string) (Event, bool) {
	if len(line) > 1024 || !utf8.ValidString(line) || strings.Contains(strings.ToLower(line), "[redacted]") || strings.IndexFunc(line, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return Event{}, false
	}
	m := nativeLine.FindStringSubmatch(line)
	if m == nil {
		return Event{}, false
	}
	kind := ""
	if advancement.MatchString(m[2]) {
		kind = "advancement"
	} else if simpleDeaths[m[2]] || attributedDeath.MatchString(m[2]) {
		kind = "death"
	}
	if kind == "" {
		return Event{}, false
	}
	return Event{Kind: kind, Player: console.PlayerIdentity{Name: m[1], Authority: "name_only", Source: "redacted_native_console"}, Message: m[1] + " " + m[2], Source: "redacted_native_console", SourceVersion: "unknown", ParserVersion: parserVersion, Confidence: "native_log_pattern"}, true
}
