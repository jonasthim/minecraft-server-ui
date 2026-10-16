package console

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var credential = regexp.MustCompile(`(?i)([a-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|credential)[a-z0-9_.-]*)["']?\s*[:=]\s*("[^"]*"|'[^']*'|[^\s,;]+)`)
var bearer = regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]+`)
var header = regexp.MustCompile(`(?i)(authorization|cookie|set-cookie)\s*[:=].*`)
var userinfo = regexp.MustCompile(`://[^/@\s]+:[^/@\s]+@`)
var query = regexp.MustCompile(`https?://[^\s?]+\?[^\s]+`)

// Redact runs before durable storage, HTTP or stream output. Exact private
// secrets supplement credential syntax; raw log bytes never leave capture.
func Redact(value string, secrets []string) string {
	ordered := append([]string(nil), secrets...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, secret := range ordered {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	value = credential.ReplaceAllString(value, "$1=[redacted]")
	value = bearer.ReplaceAllString(value, "Bearer [redacted]")
	value = header.ReplaceAllString(value, "$1: [redacted]")
	value = userinfo.ReplaceAllString(value, "://[redacted]@")
	value = query.ReplaceAllStringFunc(value, func(v string) string { return strings.SplitN(v, "?", 2)[0] + "?[redacted]" })
	return strings.Map(func(r rune) rune {
		if (unicode.IsControl(r) && r != '\t') || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(value, "�"))
}
