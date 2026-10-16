package instance

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf16"
)

// unknownProperties reads Java properties without interpreting any property as a
// path or command. A bounded decoder preserves escaped separators, Unicode and
// continued lines. Sensitive/manager-owned controls never enter Extra.
func unknownProperties(root *os.Root) (map[string]string, error) {
	return unknownPropertiesAt(root, "server/server.properties")
}
func unknownPropertiesAt(root *os.Root, name string) (map[string]string, error) {
	f, err := root.Open(name)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open properties: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("read properties: %w", err)
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("properties exceed 1 MiB")
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	out := map[string]string{}
	line := ""
	continued := false
	for scanner.Scan() {
		part := scanner.Text()
		// Natural comment lines never continue. Within an existing property
		// continuation, # and ! are ordinary key/value data instead.
		if !continued {
			trimmed := strings.TrimLeft(part, " \t\f")
			if len(trimmed) > 0 && (trimmed[0] == '#' || trimmed[0] == '!') {
				continue
			}
		}
		if continued {
			part = strings.TrimLeft(part, " \t\f")
		}
		line += part
		n := 0
		for j := len(line) - 1; j >= 0 && line[j] == '\\'; j-- {
			n++
		}
		if n%2 == 1 {
			line = line[:len(line)-1]
			continued = true
			continue
		}
		continued = false
		if err = parseProperty(line, out); err != nil {
			return nil, err
		}
		line = ""
	}
	if err = scanner.Err(); err != nil {
		return nil, err
	}
	if line != "" {
		if err = parseProperty(line, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func parseProperty(line string, out map[string]string) error {
	key, value, ok, err := decodeProperty(line)
	if err != nil {
		return err
	}
	if ok && !forbiddenProperty(key) {
		out[key] = value
	}
	return nil
}
func decodeProperty(line string) (string, string, bool, error) {
	line = strings.TrimLeft(line, " \t\f")
	if line == "" || line[0] == '#' || line[0] == '!' {
		return "", "", false, nil
	}
	end := len(line)
	escape := false
	for i := 0; i < len(line); i++ {
		if escape {
			escape = false
			continue
		}
		if line[i] == '\\' {
			escape = true
			continue
		}
		if strings.ContainsRune("=:\t\f ", rune(line[i])) {
			end = i
			break
		}
	}
	value := end
	for value < len(line) && strings.ContainsRune(" \t\f", rune(line[value])) {
		value++
	}
	if value < len(line) && (line[value] == '=' || line[value] == ':') {
		value++
	}
	for value < len(line) && strings.ContainsRune(" \t\f", rune(line[value])) {
		value++
	}
	key, err := unescapeProperty(line[:end])
	if err != nil {
		return "", "", false, err
	}
	v, err := unescapeProperty(line[value:])
	if err != nil {
		return "", "", false, err
	}
	return key, v, true, nil
}
func unescapeProperty(s string) (string, error) {
	var units []uint16
	var b strings.Builder
	flush := func() {
		if len(units) > 0 {
			b.WriteString(string(utf16.Decode(units)))
			units = nil
		}
	}
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			flush()
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			break
		}
		if s[i] == 'u' {
			if i+4 >= len(s) {
				return "", fmt.Errorf("truncated properties Unicode escape")
			}
			u, err := strconv.ParseUint(s[i+1:i+5], 16, 16)
			if err != nil {
				return "", fmt.Errorf("invalid properties Unicode escape: %w", err)
			}
			units = append(units, uint16(u))
			i += 4
			continue
		}
		flush()
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'f':
			b.WriteByte('\f')
		default:
			b.WriteByte(s[i])
		}
	}
	flush()
	return b.String(), nil
}
