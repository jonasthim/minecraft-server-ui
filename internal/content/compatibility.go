package content

import (
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
	"strconv"
	"strings"
)

// Validate the complete proposed enabled set. The same rules apply when bytes
// are installed/updated or a retained disabled artifact becomes active again.
// Unknown trials waive missing evidence only; known contradictions always block.
func enabledManifestBlockers(t Tuple, items []Installed) []string {
	blockers := []string{}
	if len(items) > 2048 {
		return []string{"Enabled content item bound exceeded"}
	}
	identities := 0
	for _, item := range items {
		identities += 1 + len(item.Descriptor.Identities)
	}
	if identities > 8192 {
		return []string{"Complete content graph exceeds 8192 identities"}
	}
	edges := 0
	for _, item := range items {
		edges += len(item.Dependencies) + len(item.Descriptor.Required) + len(item.Descriptor.Optional) + len(item.Descriptor.Conflicts) + len(item.Descriptor.Ordering)
		for _, identity := range item.Descriptor.Identities {
			edges += len(identity.Required) + len(identity.Optional) + len(identity.Conflicts)
		}
	}
	if edges > 65536 {
		return []string{"Complete content graph exceeds 65536 edges"}
	}
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
		for _, identity := range item.Descriptor.Identities {
			copy := *item
			copy.Descriptor.ID = identity.ID
			copy.Descriptor.Version = identity.Version
			descriptors[identity.ID] = append(descriptors[identity.ID], &copy)
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
				if satisfies(other.Descriptor.Version, requirement) {
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
				if satisfies(other.Descriptor.Version, requirement) {
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
		for _, identity := range append([]ModIdentity{{ID: item.Descriptor.ID, Version: item.Descriptor.Version, Optional: item.Descriptor.Optional}}, item.Descriptor.Identities...) {
			for id, requirement := range identity.Required {
				found := false
				for _, other := range descriptors[id] {
					if satisfies(other.Descriptor.Version, requirement) {
						found = true
					}
				}
				if !found {
					blockers = append(blockers, "Unresolved multi-mod required dependency: "+id)
				}
			}
			for id, requirement := range identity.Optional {
				for _, other := range descriptors[id] {
					if !satisfies(other.Descriptor.Version, requirement) {
						blockers = append(blockers, "Installed optional dependency version is incompatible: "+id)
					}
				}
			}
			for id, requirement := range identity.Conflicts {
				for _, other := range descriptors[id] {
					if satisfies(other.Descriptor.Version, requirement) {
						blockers = append(blockers, "Multi-mod incompatibility: "+id)
					}
				}
			}
		}
		for _, identity := range item.Descriptor.Identities {
			if seenDescriptors[identity.ID] {
				blockers = append(blockers, "Duplicate enabled provided identity: "+identity.ID)
			}
			seenDescriptors[identity.ID] = true
		}
		if requirement := item.Descriptor.JavaRequirement; requirement != "" {
			requirements, e := managedruntime.Select(domain.VersionSelection{Flavor: domain.Flavor(t.Loader), MinecraftVersion: t.Minecraft, LoaderVersion: t.LoaderVersion})
			if e != nil || !javaMajorSatisfies(requirements.JavaMajor, requirement) {
				blockers = append(blockers, "Declared Java requirement is incompatible or unresolved")
			}
		}
		if requirement := item.Descriptor.LanguageRequirement; requirement != "" && requirement != "*" {
			evidence := item.LanguageEvidence
			if evidence == nil || string(evidence.Selection.Flavor) != t.Loader || evidence.Selection.MinecraftVersion != t.Minecraft || evidence.Selection.LoaderVersion != t.LoaderVersion || evidence.Generation == "" || !satisfies(evidence.Languages[item.Descriptor.LanguageLoader].Version, requirement) {
				blockers = append(blockers, "Language-loader requirement lacks matching verified installation evidence")
			}
		}
		if item.Selection != nil {
			key := string(item.Selection.Provider) + "/" + item.Selection.ProjectID
			if seenProjects[key] {
				blockers = append(blockers, "Duplicate enabled publisher identity: "+key)
			}
			seenProjects[key] = true
		}
		if len(item.Dependencies) > 2048 {
			blockers = append(blockers, "Publisher dependency node limit exceeded: "+item.ID)
		}
		for index, dependency := range item.Dependencies {
			if index >= 2048 {
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
	blockers = append(blockers, orderingBlockers(items, descriptors)...)
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

// Dependency cycles can be valid; only explicitly declared loading-order cycles
// are contradictory. Kahn traversal is bounded by the complete candidate graph.
func orderingBlockers(items []Installed, descriptors map[string][]*Installed) []string {
	edges := map[string]map[string]bool{}
	indegree := map[string]int{}
	for id := range descriptors {
		indegree[id] = 0
	}
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		for _, order := range item.Descriptor.Ordering {
			if len(descriptors[order.Owner]) == 0 || len(descriptors[order.Target]) == 0 {
				continue
			}
			from, to := order.Owner, order.Target
			if order.Direction == "AFTER" {
				from, to = to, from
			}
			if edges[from] == nil {
				edges[from] = map[string]bool{}
			}
			if !edges[from][to] {
				edges[from][to] = true
				indegree[to]++
			}
		}
	}
	queue := []string{}
	for id, n := range indegree {
		if n == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for next := range edges[id] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if visited != len(indegree) {
		return []string{"Declared content loading order contains a cycle"}
	}
	return nil
}

// Runtime selection proves the Java major only. Patch-qualified constraints
// cannot be discharged from a major number and remain explicit blockers.
func javaMajorSatisfies(major int, requirement string) bool {
	for _, r := range requirement {
		if !(r >= '0' && r <= '9') && !strings.ContainsRune("[](),<>!= |&*", r) {
			return false
		}
	}
	return satisfies(strconv.Itoa(major), requirement)
}
