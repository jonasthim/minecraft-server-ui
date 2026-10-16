package modpack

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"hash"
	"io"
	"sort"
	"strings"
	"time"
)

func metadataHash(v any) string { raw, _ := json.Marshal(v); return digest(raw) }
func (s *Service) resolvePackMetadata(ctx context.Context, selection catalog.Selection) (catalog.Version, catalog.Version, catalog.Selection, error) {
	var empty catalog.Selection
	if selection.Provider != catalog.Modrinth && selection.Provider != catalog.CurseForge {
		return catalog.Version{}, catalog.Version{}, empty, invalid("choose Modrinth or CurseForge pack")
	}
	project, e := s.catalog.Project(ctx, selection.Provider, selection.ProjectID)
	if e != nil {
		return catalog.Version{}, catalog.Version{}, empty, e
	}
	if project.Kind != catalog.Modpack || project.ID != selection.ProjectID {
		return catalog.Version{}, catalog.Version{}, empty, invalid("select canonical publisher modpack")
	}
	v, e := s.catalog.Version(ctx, selection.Provider, selection.ProjectID, selection.VersionID)
	if e != nil {
		return v, v, empty, e
	}
	if v.ID != selection.VersionID || v.ProjectID != selection.ProjectID {
		return v, v, empty, invalid("publisher returned mismatched pack")
	}
	found := false
	for _, f := range v.Files {
		if f.ID == selection.FileID {
			found = true
		}
	}
	if !found {
		return v, v, empty, invalid("select exact pack file")
	}
	artifactVersion := v
	download := selection
	if selection.Provider == catalog.CurseForge && !v.IsServerPack {
		if v.ServerPackFileID == "" {
			return v, v, empty, conflict("CurseForge client pack has no official server-pack attachment")
		}
		artifactVersion, e = s.catalog.Version(ctx, selection.Provider, selection.ProjectID, v.ServerPackFileID)
		if e != nil {
			return v, v, empty, e
		}
		if !artifactVersion.IsServerPack || artifactVersion.ID != v.ServerPackFileID || artifactVersion.ProjectID != v.ProjectID || len(artifactVersion.Files) != 1 {
			return v, v, empty, invalid("official server-pack identity mismatch")
		}
		download.VersionID = artifactVersion.ID
		download.FileID = artifactVersion.Files[0].ID
	}
	return v, artifactVersion, download, nil
}
func (s *Service) Preview(ctx context.Context, id string, request Request, actor domain.User) (Preview, error) {
	needsAdmin, e := s.RequestNeedsAdministrator(ctx, id, request, actor.ID)
	if e != nil {
		return Preview{}, e
	}
	if _, e := s.actor(ctx, actor.ID, needsAdmin); e != nil {
		return Preview{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.CheckReady(ctx, id); e != nil {
		return Preview{}, e
	}
	if e := s.expire(ctx, id); e != nil {
		return Preview{}, e
	}
	manifest, e := s.content.Snapshot(ctx, id)
	if e != nil {
		return Preview{}, e
	}
	detail, e := s.instances.Get(ctx, id)
	if e != nil {
		return Preview{}, e
	}
	if detail.Provisioning != "installed" {
		return Preview{}, conflict("install exact server tuple before pack application")
	}
	var count int
	if e = s.db.QueryRowContext(ctx, "SELECT count(*) FROM modpack_previews WHERE instance_id=?", id).Scan(&count); e != nil {
		return Preview{}, e
	}
	if count >= 16 {
		return Preview{}, conflict("discard unused pack previews before creating another")
	}
	previous, e := s.Snapshot(ctx, id)
	if e != nil {
		return Preview{}, e
	}
	baseline := Baseline{Files: map[string]string{}, Selections: map[string]Selection{}, Content: map[string]string{}}
	if previous.Baseline != nil {
		baseline = *previous.Baseline
		if baseline.Source.Selection.Provider != request.Pack.Provider || baseline.Source.Selection.ProjectID != request.Pack.ProjectID {
			return Preview{}, invalid("pack update must retain original publisher project")
		}
	}
	original, artifactVersion, actual, e := s.resolvePackMetadata(ctx, request.Pack)
	if e != nil {
		return Preview{}, e
	}
	var artifact catalog.VerifiedArtifact
	if request.ArchiveReceiptID != "" {
		artifact, e = s.archiveArtifact(ctx, id, request.ArchiveReceiptID, actor.ID, request.Pack, actual, original, artifactVersion)
	} else {
		artifact, e = s.catalog.Download(ctx, actual)
	}
	if e != nil {
		if request.ArchiveReceiptID != "" || ctx.Err() != nil {
			return Preview{}, e
		}
		return s.archivePreview(ctx, id, request, actor, manifest, baseline, original, artifactVersion, actual)
	}
	f, e := s.openPackArtifact(ctx, id, artifact, request.ArchiveReceiptID)
	if e != nil {
		return Preview{}, e
	}
	defer f.Close()
	fi, e := f.Stat()
	if e != nil || !fi.Mode().IsRegular() || fi.Size() != artifact.File.Size {
		return Preview{}, invalid("verified pack artifact differs")
	}
	h := sha256.New()
	if _, e = io.Copy(h, contextReader{ctx, f}); e != nil {
		return Preview{}, e
	}
	if hex.EncodeToString(h.Sum(nil)) != artifact.SHA256 {
		return Preview{}, conflict("verified pack artifact changed")
	}
	format := "modrinth"
	if request.Pack.Provider == catalog.CurseForge {
		format = "curseforge-server"
	}
	pack, e := ParseArchive(ctx, f, fi.Size(), format)
	if e != nil {
		return Preview{}, e
	}
	if format == "curseforge-server" {
		pack.Blockers = nil
		pack.Minecraft = manifest.Tuple.Minecraft
		pack.Loader = manifest.Tuple.Loader
		pack.LoaderVersion = manifest.Tuple.LoaderVersion
		if !member(artifactVersion.Compatibility.Minecraft, pack.Minecraft) || !member(artifactVersion.Compatibility.Loaders, pack.Loader) {
			pack.Blockers = append(pack.Blockers, "Official server-pack metadata does not prove installed Minecraft and loader")
		}
	}
	if request.PreviousPreviewID != "" {
		parent, e := s.loadPreview(ctx, id, request.PreviousPreviewID, actor.ID)
		if e != nil {
			return Preview{}, e
		}
		if live, e := s.liveJob(ctx, id, parent.Preview.ID); e != nil || live != "" {
			return Preview{}, conflict("parent preview owned by live job")
		}
		if parent.Request.Pack != request.Pack || (parent.Preview.ArchiveManual == nil && parent.Preview.Source.SHA256 != artifact.SHA256) || parent.Preview.Source.ArtifactSelection != artifact.Selection || parent.Metadata != packMetadataHash(original, artifactVersion) {
			return Preview{}, conflict("parent preview pack metadata differs")
		}
		if request.Manual == nil {
			request.Manual = map[string]string{}
		}
		for name, receipt := range parent.Request.Manual {
			if request.Manual[name] == "" {
				request.Manual[name] = receipt
			}
		}
	}
	source := Source{ArtifactSelection: artifact.Selection, Selection: request.Pack, OfficialURL: original.OfficialURL, Name: original.Name, SHA256: artifact.SHA256, Provenance: artifact.Provenance}
	p := privatePreview{Preview: Preview{ID: newID(), InstanceID: id, Tuple: manifest.Tuple, Revision: manifest.Revision, BaselineRevision: baseline.Revision, Source: source, Changes: []Change{}, Selections: map[string]Selection{}, Manual: []ManualFile{}, Blockers: append([]string{}, pack.Blockers...), Excluded: pack.Excluded}, Request: request, ActorID: actor.ID, Metadata: packMetadataHash(original, artifactVersion), Manifest: manifest, Baseline: Baseline{Source: source, Files: map[string]string{}, Selections: map[string]Selection{}, Content: map[string]string{}}, Operations: []operation{}, Members: pack.Files}
	if pack.Minecraft != manifest.Tuple.Minecraft || pack.Loader != manifest.Tuple.Loader || pack.LoaderVersion != manifest.Tuple.LoaderVersion {
		p.Preview.Blockers = append(p.Preview.Blockers, "Pack requires a different exact Minecraft/loader tuple")
	}
	if original.Compatibility.ServerSide == "unsupported" {
		p.Preview.Blockers = append(p.Preview.Blockers, "Publisher marks pack version unsupported on dedicated servers")
	}
	root, e := s.openServer(ctx, id)
	if e != nil {
		return p.Preview, e
	}
	defer root.Close()
	stages := map[string]stage{}
	files := make([]string, 0, len(pack.Files))
	for name := range pack.Files {
		files = append(files, name)
	}
	sort.Strings(files)
	for _, name := range files {
		for _, installed := range manifest.Items {
			for _, owned := range installed.Files {
				if strings.TrimSuffix(owned.Path, ".disabled") == name {
					selection, saved := baseline.Selections[name]
					selection.Disabled = !installed.Enabled
					selection.Pinned = installed.Pinned
					if !saved {
						selection.Selected = true
					}
					baseline.Selections[name] = selection
					baseline.Content[name] = installed.ID
				}
			}
		}
		file := pack.Files[name]
		selection, exists := baseline.Selections[name]
		if !exists {
			selection = Selection{Selected: file.Server == "required", Optional: file.Server == "optional"}
		}
		if provided, ok := request.Selections[name]; ok {
			selection = provided
			if installedID := baseline.Content[name]; installedID != "" {
				selection.Disabled = baseline.Selections[name].Disabled
			}
		}
		selection.Optional = file.Server == "optional"
		if !selection.Optional && !selection.Selected && !selection.Excluded {
			return p.Preview, invalid("required server files must be selected or explicitly excluded")
		}
		p.Preview.Selections[name] = selection
		p.Baseline.Selections[name] = selection
		if selection.Excluded || selection.Optional && !selection.Selected {
			continue
		}
		var st stage
		if file.ArchiveEntry != "" {
			if file.Bytes != nil {
				st, e = s.stage(ctx, id, actor.ID, bytes.NewReader(file.Bytes), int64(len(file.Bytes)))
			} else {
				z, ze := zip.NewReader(f, fi.Size())
				if ze != nil {
					return p.Preview, ze
				}
				for _, entry := range z.File {
					if entry.Name == file.ArchiveEntry {
						r, re := entry.Open()
						if re != nil {
							return p.Preview, re
						}
						st, e = s.stage(ctx, id, actor.ID, r, file.Size)
						r.Close()
						break
					}
				}
			}
		} else if upload := request.Manual[name]; upload != "" {
			var receipt stage
			var owner int64
			lookup := s.db.QueryRowContext(ctx, "SELECT id,sha256,size,actor_id FROM modpack_stages WHERE instance_id=? AND id=?", id, upload).Scan(&receipt.ID, &receipt.SHA256, &receipt.Size, &owner)
			if lookup == nil && owner == actor.ID {
				rf, re := s.openStage(ctx, id, receipt)
				if re != nil {
					return p.Preview, re
				}
				e = verifyPackMember(ctx, rf, receipt.Size, file)
				rf.Close()
				st = receipt
			} else {
				e = s.content.WithVerifiedPackUpload(ctx, id, upload, actor, file.Hashes, func(r io.ReaderAt, size int64) error {
					if size != file.Size {
						return invalid("manual artifact size differs from exact pack file")
					}
					var err error
					st, err = s.stage(ctx, id, actor.ID, io.NewSectionReader(r, 0, size), size)
					return err
				})
			}
		} else {
			st, e = s.downloadPackFile(ctx, id, actor.ID, file)
		}
		if e != nil {
			p.Preview.Manual = append(p.Preview.Manual, ManualFile{Path: name, OfficialURL: source.OfficialURL, Hashes: file.Hashes, Reason: "Exact file requires digest-verified administrator upload"})
			continue
		}
		if st.ID == "" {
			return p.Preview, invalid("pack file has no staged bytes")
		}
		if e = s.validateDestinationStage(ctx, id, name, st); e != nil {
			return p.Preview, e
		}
		stages[name] = st
		p.Baseline.Files[name] = st.SHA256
	}
	for name := range request.Selections {
		if _, ok := pack.Files[name]; !ok {
			return p.Preview, invalid("selection path is not a server pack member")
		}
	}
	for name := range request.Manual {
		if _, ok := pack.Files[name]; !ok {
			return p.Preview, invalid("manual upload is not bound to a server pack member")
		}
	}
	local := map[string]string{}
	for name := range baseline.Files {
		hash, _, e := currentHash(ctx, root, physicalPath(name, baseline.Selections[name]), MaxFileBytes)
		if e != nil {
			return p.Preview, e
		}
		local[name] = hash
	}
	for name := range p.Baseline.Files {
		if _, ok := local[name]; !ok {
			hash, _, e := currentHash(ctx, root, physicalPath(name, p.Preview.Selections[name]), MaxFileBytes)
			if e != nil {
				return p.Preview, e
			}
			local[name] = hash
		}
	}
	for name, sel := range baseline.Selections {
		if _, ok := p.Preview.Selections[name]; !ok {
			p.Preview.Selections[name] = sel
			p.Baseline.Selections[name] = sel
		}
	}
	p.Preview.Changes, e = Compare(baseline.Files, local, p.Baseline.Files, p.Preview.Selections, request.Choices)
	if e != nil {
		return p.Preview, invalid(e.Error())
	}
	candidate := map[string]content.Installed{}
	for _, item := range manifest.Items {
		candidate[item.ID] = item
	}
	changes := 0
	customized := map[string]bool{}
	for name, selection := range p.Preview.Selections {
		if selection.Pinned || selection.Excluded || selection.Disabled || selection.Optional {
			customized[name] = true
		}
	}
	for _, change := range p.Preview.Changes {
		name := change.Path
		if change.Action == "conflict" {
			p.Preview.Blockers = append(p.Preview.Blockers, "Resolve conflict: "+name)
			continue
		}
		if change.Action == "keep" {
			if change.Before != change.Upstream {
				customized[name] = true
			}
			if change.Upstream == "" && change.Before != "" {
				p.Baseline.Files[name] = ""
			}
			if oldID := baseline.Content[name]; oldID != "" {
				if item, ok := candidate[oldID]; ok {
					item.Pinned = p.Preview.Selections[name].Pinned
					candidate[oldID] = item
				}
				p.Baseline.Content[name] = oldID
			}
			continue
		}
		changes++
		actual := physicalPath(name, p.Preview.Selections[name])
		before := stage{}
		if change.Before != "" {
			file, e := openChecked(root, actual, MaxFileBytes)
			if e != nil {
				return p.Preview, e
			}
			info, e := file.Stat()
			if e != nil {
				file.Close()
				return p.Preview, e
			}
			before, e = s.stage(ctx, id, actor.ID, file, info.Size())
			file.Close()
			if e != nil {
				return p.Preview, e
			}
			if before.SHA256 != change.Before {
				return p.Preview, conflict("local bytes changed during preview")
			}
		}
		after := stages[name]
		p.Operations = append(p.Operations, operation{Path: actual, Before: before, After: after})
		if oldID := baseline.Content[name]; oldID != "" {
			delete(candidate, oldID)
		}
		if strings.HasPrefix(name, "mods/") && change.Action != "delete" {
			file, e := s.openStage(ctx, id, after)
			if e != nil {
				return p.Preview, e
			}
			descriptor, e := content.InspectPackReader(ctx, file, after.Size)
			file.Close()
			if e != nil {
				return p.Preview, e
			}
			itemID := baseline.Content[name]
			if itemID == "" {
				itemID = newID()
			}
			fileMeta := pack.Files[name]
			item := content.Installed{ID: itemID, Kind: catalog.Mod, Name: descriptor.ID, Version: descriptor.Version, SHA256: after.SHA256, Files: []content.OwnedFile{{Path: actual, SHA256: after.SHA256}}, Enabled: !p.Preview.Selections[name].Disabled, Pinned: p.Preview.Selections[name].Pinned, Unknown: descriptor.Unknown, Descriptor: descriptor, Dependencies: []catalog.Dependency{}, PackMember: &content.PackProvenance{Selection: request.Pack, PackSHA256: artifact.SHA256, Path: name, Hashes: fileMeta.Hashes, MetadataSource: string(request.Pack.Provider), FetchedAt: artifact.FetchedAt.UTC().Format(time.RFC3339Nano)}}
			candidate[itemID] = item
			p.Baseline.Content[name] = itemID
		}
	}
	for _, item := range candidate {
		p.Candidate = append(p.Candidate, item)
	}
	sort.Slice(p.Candidate, func(i, j int) bool { return p.Candidate[i].ID < p.Candidate[j].ID })
	var packBlockers []string
	p.Candidate, packBlockers, e = s.content.PreparePackCandidate(ctx, id, manifest.Tuple, p.Candidate)
	if e != nil {
		return p.Preview, e
	}
	p.Preview.Blockers = append(p.Preview.Blockers, packBlockers...)
	p.Preview.Summary = fmt.Sprintf("%d proposed file changes; %d preserved customizations; %d manual files; %d excluded client/control entries", changes, len(customized), len(p.Preview.Manual), len(p.Preview.Excluded))
	p.Baseline.Revision = metadataHash(p.Baseline)
	return s.storePreview(ctx, p)
}
func physicalPath(name string, s Selection) string {
	if s.Disabled && strings.HasPrefix(name, "mods/") {
		return name + ".disabled"
	}
	return name
}
func member(values []string, v string) bool {
	for _, s := range values {
		if s == v {
			return true
		}
	}
	return false
}
func publishedHashes(hashes map[string]string) (map[string]hash.Hash, error) {
	out := map[string]hash.Hash{}
	for name, value := range hashes {
		switch name {
		case "sha1":
			if !validHex(value, 40) {
				return nil, invalid("invalid SHA1")
			}
			out[name] = sha1.New()
		case "sha256":
			if !validHex(value, 64) {
				return nil, invalid("invalid SHA256")
			}
			out[name] = sha256.New()
		case "sha512":
			if !validHex(value, 128) {
				return nil, invalid("invalid SHA512")
			}
			out[name] = sha512.New()
		}
	}
	if len(out) == 0 {
		return nil, invalid("published digest required")
	}
	return out, nil
}

func packMetadataHash(versions ...catalog.Version) string {
	for i := range versions {
		versions[i].FetchedAt = time.Time{}
	}
	return metadataHash(versions)
}
