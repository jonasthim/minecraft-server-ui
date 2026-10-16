package modpack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sort"
	"time"
)

const summaryByteBudget int64 = 16 << 20
const summaryFileBudget = 256

// A viewer never waits for scan capacity. Repeated reads reuse a short observed
// snapshot; uncertainty is visible instead of counting unexamined bytes unchanged.
var summaryScanSlots = make(chan struct{}, 2)

type summaryCacheEntry struct {
	key     string
	expires time.Time
	value   CustomizationSummary
}

func emptyCustomizationSummary() CustomizationSummary {
	return CustomizationSummary{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Entries: []Customization{}, Complete: true, Notices: []string{}}
}
func (s *Service) currentCustomizations(ctx context.Context, id string, baseline *Baseline) CustomizationSummary {
	out := emptyCustomizationSummary()
	if baseline == nil {
		return out
	}
	out.Source = &baseline.Source
	out.TotalFiles = len(baseline.Files)
	if len(baseline.Files) > MaxPackFiles || len(baseline.Selections) > MaxPackFiles || len(baseline.Content) > MaxPackFiles {
		out.TotalFiles = min(out.TotalFiles, MaxPackFiles)
		out.Complete = false
		out.Notices = append(out.Notices, "Applied baseline exceeds the supported summary bound.")
		return out
	}
	scanCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	entries := map[string]Customization{}
	add := func(name, reason string, observed bool, local, upstream string) {
		entry := entries[name]
		entry.Path = name
		entry.Reasons = append(entry.Reasons, reason)
		if observed {
			entry.LocalSHA256 = local
			entry.UpstreamSHA256 = upstream
			if entry.Evidence == "saved_selection" {
				entry.Evidence = "saved_selection_and_observed_file"
			} else {
				entry.Evidence = "observed_file"
			}
		} else {
			entry.Evidence = "saved_selection"
		}
		entries[name] = entry
	}
	selections := make(map[string]Selection, len(baseline.Selections))
	for name, selection := range baseline.Selections {
		selections[name] = selection
	}
	// Task16 controls can change installed pin/disabled metadata after pack apply.
	// They are read independently of the bounded filesystem observation.
	contentNames := map[string][]string{}
	for name, itemID := range baseline.Content {
		contentNames[itemID] = append(contentNames[itemID], name)
	}
	if manifest, e := s.content.Snapshot(scanCtx, id); e == nil {
		for _, item := range manifest.Items {
			for _, owned := range item.Files {
				for _, name := range contentNames[item.ID] {
					if physicalPath(name, Selection{Disabled: !item.Enabled}) == owned.Path {
						selection := selections[name]
						selection.Pinned = item.Pinned
						selection.Disabled = !item.Enabled
						selections[name] = selection
					}
				}
			}
		}
	} else {
		out.Complete = false
		out.Notices = append(out.Notices, "Current installed item metadata could not be checked.")
	}
	for name, selection := range selections {
		if !allowedPath(name) {
			out.Complete = false
			continue
		}
		if selection.Pinned {
			add(name, "pinned", false, "", "")
		}
		if selection.Excluded {
			add(name, "excluded", false, "", "")
		}
		if selection.Disabled {
			add(name, "disabled", false, "", "")
		}
		if selection.Optional {
			if selection.Selected {
				add(name, "optional_selected", false, "", "")
			} else {
				add(name, "optional_deselected", false, "", "")
			}
		}
	}
	finish := func() CustomizationSummary {
		keys := make([]string, 0, len(entries))
		for name := range entries {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, name := range keys {
			if len(out.Entries) == 64 {
				out.Truncated = true
				break
			}
			out.Entries = append(out.Entries, entries[name])
		}
		if out.Truncated {
			out.Notices = append(out.Notices, "Showing the first 64 customization paths.")
		}
		return out
	}
	if len(baseline.Files) > MaxPackFiles || len(selections) > MaxPackFiles {
		out.Complete = false
		out.Notices = append(out.Notices, "Applied baseline exceeds the supported summary bound.")
		return finish()
	}
	key := metadataHash(struct {
		Baseline   *Baseline
		Selections map[string]Selection
	}{baseline, selections})
	s.summaryMu.Lock()
	cached, ok := s.summaryCache[id]
	s.summaryMu.Unlock()
	if ok && cached.key == key && time.Now().Before(cached.expires) {
		return cached.value
	}
	select {
	case summaryScanSlots <- struct{}{}:
		defer func() { <-summaryScanSlots }()
	default:
		out.Complete = false
		out.Notices = append(out.Notices, "Current file observation is busy; saved selections are shown. Refresh shortly.")
		return finish()
	}
	root, e := s.openServer(scanCtx, id)
	if e != nil {
		out.Complete = false
		out.Notices = append(out.Notices, "Current pack files could not be observed safely.")
		return finish()
	}
	defer root.Close()
	names := make([]string, 0, len(baseline.Files))
	for name := range baseline.Files {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := textDestination(names[i]), textDestination(names[j])
		if a != b {
			return a
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		if !allowedPath(name) || out.CheckedFiles >= summaryFileBudget || out.HashedBytes >= summaryByteBudget || scanCtx.Err() != nil {
			out.Complete = false
			continue
		}
		remaining := summaryByteBudget - out.HashedBytes
		max := int64(8 << 20)
		if remaining < max {
			max = remaining
		}
		hash, size, err := observeCustomizationFile(scanCtx, root, physicalPath(name, selections[name]), max)
		out.HashedBytes += size
		if err != nil {
			out.Complete = false
			continue
		}
		out.CheckedFiles++
		upstream := baseline.Files[name]
		if hash != upstream {
			reason := "edited"
			if hash == "" {
				reason = "deleted"
			} else if upstream == "" {
				reason = "retained"
			}
			add(name, reason, true, hash, upstream)
		}
	}
	if !out.Complete {
		out.Notices = append(out.Notices, "File observation is incomplete; unexamined files are not counted as unchanged.")
	}
	out = finish()
	s.summaryMu.Lock()
	if s.summaryCache == nil {
		s.summaryCache = map[string]summaryCacheEntry{}
	}
	if len(s.summaryCache) >= 32 {
		for instanceID := range s.summaryCache {
			delete(s.summaryCache, instanceID)
			break
		}
	}
	s.summaryCache[id] = summaryCacheEntry{key: key, expires: time.Now().Add(2 * time.Second), value: out}
	s.summaryMu.Unlock()
	return out
}

// Reuse checked-parent/link/identity opening, then hash only this request's budget.
func observeCustomizationFile(ctx context.Context, root *os.Root, name string, max int64) (string, int64, error) {
	f, e := openChecked(root, name, max)
	if os.IsNotExist(e) {
		return "", 0, nil
	}
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	before, e := f.Stat()
	if e != nil {
		return "", 0, e
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(contextReader{ctx, f}, max))
	if e != nil {
		return "", n, e
	}
	after, e := f.Stat()
	if e != nil {
		return "", n, e
	}
	if n != before.Size() || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return "", n, conflict("file changed during observation")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
