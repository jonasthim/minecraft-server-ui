package modpack

import (
	"path"
	"strings"
	"unicode/utf8"
)

func safeRelative(p string) bool {
	if p == "" || len(p) > 900 || !utf8.ValidString(p) || len(strings.Split(p, "/")) > 12 {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 160 || strings.Trim(part, " .") != part || strings.ContainsAny(part, "\\:<>\"|?*\x00") {
			return false
		}
		for _, r := range part {
			if r < 32 || r == 127 {
				return false
			}
		}
		stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || stem == "CONIN$" || stem == "CONOUT$" {
			return false
		}
		if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
			return false
		}
	}
	return true
}
func allowedPath(p string) bool {
	if !safeRelative(p) {
		return false
	}
	parts := strings.Split(p, "/")
	if len(parts) < 2 {
		return false
	}
	for _, v := range parts {
		l := strings.ToLower(v)
		if strings.HasPrefix(v, ".") || l == "world" || l == "worlds" || strings.HasPrefix(l, "world_") || l == "runtime" || l == "runtimes" || l == "state" || l == "logs" || l == "backups" || l == "libraries" || l == "manager.db" || l == "server.properties" || l == "eula.txt" || strings.Contains(l, "credential") || strings.Contains(l, "secret") || strings.Contains(l, "rcon") || strings.Contains(l, "management") {
			return false
		}
	}
	ext := strings.ToLower(path.Ext(p))
	if parts[0] == "mods" {
		return len(parts) == 2 && ext == ".jar"
	}
	if parts[0] == "scripts" {
		return ext == ".zs"
	}
	if parts[0] == "kubejs" {
		if len(parts) > 2 && parts[1] == "assets" && (ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".ogg") {
			return true
		}
		if len(parts) > 2 && parts[1] == "data" && ext == ".nbt" {
			return true
		}
		return len(parts) > 2 && (parts[1] == "server_scripts" || parts[1] == "startup_scripts" || parts[1] == "data" || parts[1] == "assets" || parts[1] == "config") && (ext == ".js" || ext == ".json" || ext == ".json5" || ext == ".toml" || ext == ".properties" || ext == ".txt" || ext == ".mcmeta")
	}
	if parts[0] != "config" && parts[0] != "defaultconfigs" {
		return false
	}
	switch ext {
	case ".toml", ".json", ".json5", ".yaml", ".yml", ".properties", ".cfg", ".conf", ".txt":
		return true
	}
	return false
}

func binaryResource(p string) bool {
	if !allowedPath(p) {
		return false
	}
	ext := strings.ToLower(path.Ext(p))
	return strings.HasPrefix(p, "kubejs/assets/") && (ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".ogg") || strings.HasPrefix(p, "kubejs/data/") && ext == ".nbt"
}
