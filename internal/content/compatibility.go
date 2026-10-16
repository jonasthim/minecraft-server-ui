package content

import "github.com/jonasthim/minecraft-server-ui/internal/catalog"

// Validate the complete proposed enabled set. The same rules apply when bytes
// are installed/updated or a retained disabled artifact becomes active again.
// Unknown trials waive missing evidence only; known contradictions always block.
func enabledManifestBlockers(t Tuple, items []Installed) []string {
	blockers := []string{}
	descriptors := map[string][]*Installed{}
	projects := map[string][]*Installed{}
	versions := map[string][]*Installed{}
	for i := range items {
		item := &items[i]
		if !item.Enabled {
			continue
		}
		if item.Descriptor.ID != "" {
			descriptors[item.Descriptor.ID] = append(descriptors[item.Descriptor.ID], item)
		}
		if item.Selection != nil {
			selection := item.Selection
			project := string(selection.Provider) + "/" + selection.ProjectID
			version := string(selection.Provider) + "/" + selection.VersionID
			projects[project] = append(projects[project], item)
			versions[version] = append(versions[version], item)
		}
	}
	seenDescriptors := map[string]bool{}
	seenProjects := map[string]bool{}
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		if item.Kind == catalog.Plugin && t.Loader != "paper" || item.Kind == catalog.Mod && !contains([]string{"fabric", "forge", "neoforge"}, t.Loader) {
			blockers = append(blockers, "Enabled content kind does not match the installed server type: "+item.ID)
		}
		trial := item.Unknown && item.TrialTuple != nil && *item.TrialTuple == t
		if item.Unknown && !trial {
			blockers = append(blockers, "Unknown trial belongs to a different exact server tuple: "+item.ID)
		}
		// Static tuple/evidence checks share the descriptor checker; dependencies use
		// indexes so a normal large pack does not allocate one copied manifest/item.
		descriptor := item.Descriptor
		descriptor.Required = nil
		descriptor.Conflicts = nil
		blockers = append(blockers, compatibilityBlockers(descriptor, t, item.Kind, nil, trial)...)
		for id, requirement := range item.Descriptor.Required {
			found := false
			for _, other := range descriptors[id] {
				if other.ID != item.ID && satisfies(other.Descriptor.Version, requirement) {
					found = true
					break
				}
			}
			if !found {
				blockers = append(blockers, "Unresolved required descriptor dependency: "+id)
			}
		}
		for id, requirement := range item.Descriptor.Conflicts {
			for _, other := range descriptors[id] {
				if other.ID != item.ID && satisfies(other.Descriptor.Version, requirement) {
					blockers = append(blockers, "Installed descriptor incompatibility: "+id)
					break
				}
			}
		}
		if item.Descriptor.ID != "" {
			if seenDescriptors[item.Descriptor.ID] {
				blockers = append(blockers, "Duplicate enabled descriptor identity: "+item.Descriptor.ID)
			}
			seenDescriptors[item.Descriptor.ID] = true
		}
		if item.Selection != nil {
			key := string(item.Selection.Provider) + "/" + item.Selection.ProjectID
			if seenProjects[key] {
				blockers = append(blockers, "Duplicate enabled publisher identity: "+key)
			}
			seenProjects[key] = true
		}
		if len(item.Dependencies) > 128 {
			blockers = append(blockers, "Publisher dependency node limit exceeded: "+item.ID)
		}
		for index, dependency := range item.Dependencies {
			if index >= 128 {
				break
			}
			matches := projects[string(dependency.Provider)+"/"+dependency.ProjectID]
			if dependency.ProjectID == "" {
				matches = versions[string(dependency.Provider)+"/"+dependency.VersionID]
			}
			found := false
			for _, other := range matches {
				if other.ID != item.ID && dependencyMatches(dependency, *other.Selection) {
					found = true
					break
				}
			}
			if dependency.Required && !found {
				blockers = append(blockers, "Unresolved required publisher dependency: "+dependencyLabel(dependency))
			}
			if dependency.Relation == "incompatible" && found {
				blockers = append(blockers, "Publisher declares an enabled incompatibility: "+dependencyLabel(dependency))
			}
		}
	}
	return blockers
}
func proposedEnabledBlockers(t Tuple, items []Installed, p privatePreview) []string {
	if p.Next == nil || p.Mutation.Action != "install" && p.Mutation.Action != "update" && p.Mutation.Action != "enable" {
		return nil
	}
	proposed := make([]Installed, 0, len(items)+1)
	replaced := false
	for _, item := range items {
		if item.ID == p.Next.ID {
			proposed = append(proposed, *p.Next)
			replaced = true
		} else {
			proposed = append(proposed, item)
		}
	}
	if !replaced {
		proposed = append(proposed, *p.Next)
	}
	return enabledManifestBlockers(t, proposed)
}
