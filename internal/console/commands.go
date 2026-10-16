// Package console owns redacted console history and constrained server control.
package console

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
)

// PublicCommand is validated against the reviewed version family. It is never
// an execution acknowledgement or evidence that a plugin preserves semantics.
type PublicCommand struct {
	Verb string
	Text string
}

// ValidateCommand deliberately accepts a small grammar. Lifecycle, save leases,
// settings and player privileges belong to typed feature operations.
func ValidateCommand(selection domain.VersionSelection, role domain.Role, text string) (PublicCommand, error) {
	if !role.Valid() || !role.AtLeast(domain.RoleOperator) {
		return PublicCommand{}, domain.E(domain.CodeForbidden, "operator access required")
	}
	if _, err := managedruntime.Select(selection); err != nil {
		return PublicCommand{}, domain.E(domain.CodeUnsupported, "command behavior is unavailable for this version and server type")
	}
	if len(text) == 0 || len(text) > 516 || !utf8.ValidString(text) || strings.TrimSpace(text) != text {
		return PublicCommand{}, domain.E(domain.CodeValidationFailed, "command must be one bounded line without surrounding whitespace")
	}
	for _, r := range text {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return PublicCommand{}, domain.E(domain.CodeValidationFailed, "command contains unsupported control characters")
		}
	}
	verb, body, hasBody := strings.Cut(text, " ")
	switch verb {
	case "list":
		if hasBody {
			return PublicCommand{}, domain.E(domain.CodeUnsupported, "list takes no arguments")
		}
	case "seed":
		if role != domain.RoleAdmin {
			return PublicCommand{}, domain.E(domain.CodeForbidden, "administrator access required for seed")
		}
		if hasBody {
			return PublicCommand{}, domain.E(domain.CodeUnsupported, "seed takes no arguments")
		}
	case "say":
		if !hasBody || body == "" || len(body) > 512 || strings.TrimSpace(body) != body || strings.Contains(body, "@") {
			return PublicCommand{}, domain.E(domain.CodeValidationFailed, "announcement must contain 1–512 bytes of plain text without selectors")
		}
	default:
		return PublicCommand{}, domain.E(domain.CodeUnsupported, "this command is unavailable; use the dedicated instance controls")
	}
	return PublicCommand{Verb: verb, Text: text}, nil
}
