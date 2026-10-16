// Package modpack parses inert publisher packs and preserves three-way customizations.
package modpack

import (
	"fmt"
	"sort"
	"strings"
)

type Selection struct {
	Optional bool `json:"optional"`
	Selected bool `json:"selected"`
	Pinned   bool `json:"pinned"`
	Excluded bool `json:"excluded"`
	Disabled bool `json:"disabled"`
}
type Change struct {
	Path     string `json:"path"`
	Baseline string `json:"baseline"`
	Before   string `json:"before"`
	Upstream string `json:"upstream"`
	Action   string `json:"action"`
	Reason   string `json:"reason"`
	Choice   string `json:"choice"`
}

// Compare treats an absent/empty hash as a missing file. Choices are bound to an
// existing path; keep preserves bytes including deliberate local deletion.
func Compare(old, local, next map[string]string, selections map[string]Selection, choices map[string]string) ([]Change, error) {
	paths := map[string]bool{}
	folded := map[string]string{}
	for _, set := range []map[string]string{old, local, next} {
		for p := range set {
			if !safeRelative(p) {
				return nil, fmt.Errorf("unsafe pack path")
			}
			k := strings.ToLower(p)
			if previous, ok := folded[k]; ok && previous != p {
				return nil, fmt.Errorf("case-colliding pack paths")
			}
			folded[k] = p
			paths[p] = true
		}
	}
	for p, c := range choices {
		if !paths[p] || (c != "keep" && c != "upstream") {
			return nil, fmt.Errorf("unbound or invalid conflict choice")
		}
	}
	keys := make([]string, 0, len(paths))
	for p := range paths {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	changes := make([]Change, 0, len(keys))
	for _, p := range keys {
		c := Change{Path: p, Baseline: old[p], Before: local[p], Upstream: next[p], Action: "keep", Reason: "unchanged"}
		selection := selections[p]
		switch {
		case selection.Pinned:
			c.Reason = "pinned local item"
		case selection.Excluded:
			c.Reason = "persistent user exclusion"
		case selection.Optional && !selection.Selected:
			c.Reason = "optional item not selected"
		case choices[p] == "keep":
			c.Reason = "explicitly preserve local state"
			c.Choice = "keep"
		case choices[p] == "upstream":
			c.Action = chooseOperation(local[p], next[p])
			c.Reason = "explicit upstream choice"
			c.Choice = "upstream"
		case local[p] == next[p]:
			c.Reason = "local state matches upstream"
		case old[p] == next[p]:
			c.Reason = "preserved local customization"
		case local[p] == old[p]:
			c.Action = chooseOperation(local[p], next[p])
			c.Reason = "upstream changed; local baseline intact"
		default:
			c.Action = "conflict"
			c.Reason = "local and upstream diverged; choose keep or upstream"
		}
		changes = append(changes, c)
	}
	return changes, nil
}
func chooseOperation(before, after string) string {
	if before == after {
		return "keep"
	}
	if after == "" {
		return "delete"
	}
	if before == "" {
		return "add"
	}
	return "update"
}
