package players

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io"
	"os"
	"runtime"
	"strings"
	"time"
)

const maxListBytes = 4 << 20

func filename(kind string) (string, error) {
	switch kind {
	case "whitelist":
		return "whitelist.json", nil
	case "bans":
		return "banned-players.json", nil
	case "operators":
		return "ops.json", nil
	}
	return "", domain.E(domain.CodeValidationFailed, "invalid player list")
}
func mutationKind(action string) string {
	switch action {
	case "whitelist_add", "whitelist_remove":
		return "whitelist"
	case "ban", "unban":
		return "bans"
	case "op", "deop":
		return "operators"
	}
	return ""
}
func readStable(ctx context.Context, root *os.Root, name string) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	info, e := root.Lstat(name)
	if os.IsNotExist(e) {
		return []byte("[]"), nil
	}
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Size() > maxListBytes {
		return nil, domain.E(domain.CodeUnavailable, "saved player list is unsafe or oversized")
	}
	f, e := root.Open(name)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	stat, e := f.Stat()
	if e != nil || !os.SameFile(info, stat) {
		return nil, domain.E(domain.CodeUnavailable, "saved player list changed during opening")
	}
	b, e := io.ReadAll(io.LimitReader(f, maxListBytes+1))
	if e != nil {
		return nil, e
	}
	if len(b) > maxListBytes {
		return nil, domain.E(domain.CodeUnavailable, "saved player list exceeds size limit")
	}
	if _, e = f.Seek(0, 0); e != nil {
		return nil, e
	}
	again, e := io.ReadAll(io.LimitReader(f, maxListBytes+1))
	after, e2 := root.Lstat(name)
	if e != nil || e2 != nil || !os.SameFile(stat, after) || !bytes.Equal(b, again) {
		return nil, domain.E(domain.CodeUnavailable, "saved player list changed while reading")
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	return b, nil
}

type savedEntry struct {
	raw        json.RawMessage
	fields     map[string]json.RawMessage
	uuid, name string
}

func parseSaved(b []byte) ([]savedEntry, error) {
	var raw []json.RawMessage
	if json.Unmarshal(b, &raw) != nil || raw == nil || len(raw) > 10000 {
		return nil, domain.E(domain.CodeUnavailable, "saved player list is malformed or exceeds entry limits")
	}
	out := make([]savedEntry, 0, len(raw))
	ids, names := map[string]bool{}, map[string]bool{}
	for _, r := range raw {
		var fields map[string]json.RawMessage
		var uuid, name string
		if decodeSavedObject(r, &fields) != nil || fields == nil || json.Unmarshal(fields["uuid"], &uuid) != nil || json.Unmarshal(fields["name"], &name) != nil || uuid == "" || name == "" || console.ValidatePlayerIdentity(uuid, name) != nil || ids[uuid] || names[strings.ToLower(name)] {
			return nil, domain.E(domain.CodeUnavailable, "saved player identity is malformed or ambiguous")
		}
		ids[uuid] = true
		names[strings.ToLower(name)] = true
		out = append(out, savedEntry{r, fields, uuid, name})
	}
	return out, nil
}
func savedList(ctx context.Context, root *os.Root, kind string) (console.PlayerList, error) {
	out := console.PlayerList{Kind: kind, Entries: []console.PlayerListEntry{}, Source: "saved_native_json", ObservedAt: time.Now().UTC(), RuntimeVerified: false}
	name, e := filename(kind)
	if e != nil {
		return out, e
	}
	b, e := readStable(ctx, root, name)
	if e != nil {
		return out, e
	}
	entries, e := parseSaved(b)
	if e != nil {
		return out, e
	}
	for _, p := range entries {
		entry := console.PlayerListEntry{Player: console.PlayerIdentity{UUID: p.uuid, Name: p.name, Authority: "saved_uuid", Source: "saved_native_json"}}
		if kind == "operators" {
			var level int
			var bypass bool
			if json.Unmarshal(p.fields["level"], &level) != nil || level < 0 || level > 4 || json.Unmarshal(p.fields["bypassesPlayerLimit"], &bypass) != nil {
				return out, domain.E(domain.CodeUnavailable, "saved operator permission fields are malformed")
			}
			entry.PermissionLevel = &level
			entry.BypassesPlayerLimit = &bypass
		}
		out.Entries = append(out.Entries, entry)
	}
	return out, nil
}
func mutateSaved(ctx context.Context, root *os.Root, m console.PlayerMutation, now time.Time) (resultErr error) {
	if e := console.ValidatePlayerMutation(m); e != nil {
		return e
	}
	kind := mutationKind(m.Action)
	name, e := filename(kind)
	if e != nil {
		return e
	}
	if m.UUID == "" {
		return domain.E(domain.CodeValidationFailed, "offline list mutation requires an explicit UUID")
	}
	original, e := readStable(ctx, root, name)
	if e != nil {
		return e
	}
	entries, e := parseSaved(original)
	if e != nil {
		return e
	}
	if _, e = savedList(ctx, root, kind); e != nil {
		return e
	}
	idx := -1
	for i, p := range entries {
		if p.uuid == m.UUID {
			idx = i
			if m.Name != "" && !strings.EqualFold(m.Name, p.name) {
				return domain.E(domain.CodeConflict, "UUID and username disagree with saved identity")
			}
		} else if m.Name != "" && strings.EqualFold(m.Name, p.name) {
			return domain.E(domain.CodeConflict, "username already belongs to another saved UUID")
		}
	}
	add := m.Action == "whitelist_add" || m.Action == "ban" || m.Action == "op"
	if add && m.Name == "" {
		return domain.E(domain.CodeValidationFailed, "offline additions require UUID and username")
	}
	if add && idx < 0 && len(entries) >= 10000 {
		return domain.E(domain.CodeConflict, "saved player list entry limit reached")
	}
	raws := make([]json.RawMessage, 0, len(entries)+1)
	for i, p := range entries {
		if i == idx {
			if !add {
				continue
			}
			if m.Action != "op" {
				raws = append(raws, p.raw)
				continue
			}
			level := 4
			if m.PermissionLevel != nil {
				level = *m.PermissionLevel
			}
			p.fields["level"], _ = json.Marshal(level)
			b, _ := json.Marshal(p.fields)
			raws = append(raws, b)
		} else {
			raws = append(raws, p.raw)
		}
	}
	if add && idx < 0 {
		fields := map[string]any{"uuid": m.UUID, "name": m.Name}
		if kind == "operators" {
			level := 4
			if m.PermissionLevel != nil {
				level = *m.PermissionLevel
			}
			fields["level"] = level
			fields["bypassesPlayerLimit"] = false
		}
		if kind == "bans" {
			fields["created"] = now.UTC().Format("2006-01-02 15:04:05 -0700")
			fields["source"] = "Server Manager"
			fields["expires"] = "forever"
			fields["reason"] = "Banned by an operator."
		}
		b, _ := json.Marshal(fields)
		raws = append(raws, b)
	}
	// Keep each unrelated entry's original bytes, including unknown fields and
	// object whitespace. JSON has no native comments; invalid commented files fail.
	var out bytes.Buffer
	out.WriteString("[\n")
	for i, r := range raws {
		if i > 0 {
			out.WriteString(",\n")
		}
		out.Write(r)
	}
	out.WriteString("\n]\n")
	if out.Len() > maxListBytes {
		return domain.E(domain.CodeConflict, "saved player list exceeds size limit")
	}
	if bytes.Equal(original, out.Bytes()) || (!add && idx < 0) || (add && idx >= 0 && m.Action != "op") {
		return nil
	}
	var nonce [12]byte
	if _, e = rand.Read(nonce[:]); e != nil {
		return e
	}
	tmp := ".manager-players-" + hex.EncodeToString(nonce[:]) + ".tmp"
	f, e := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer func() {
		if cleanupErr := root.Remove(tmp); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
			resultErr = errors.Join(resultErr, domain.E(domain.CodeUnavailable, "saved player staging cleanup failed; retained staging requires inspection"))
		}
	}()
	_, e = f.Write(out.Bytes())
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return fmt.Errorf("stage saved player list: %w", e)
	}
	current, e := readStable(ctx, root, name)
	if e != nil {
		return e
	}
	if sha256.Sum256(current) != sha256.Sum256(original) {
		return domain.E(domain.CodeConflict, "saved list changed before activation")
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if e = root.Rename(tmp, name); e != nil {
		return fmt.Errorf("activate saved player list: %w", e)
	}
	if runtime.GOOS == "linux" {
		dir, e := root.Open(".")
		if e != nil {
			return e
		}
		e = errors.Join(dir.Sync(), dir.Close())
		if e != nil {
			return fmt.Errorf("saved player list activated but directory durability is unknown: %w", e)
		}
	}
	return nil
}

func decodeSavedObject(raw []byte, fields *map[string]json.RawMessage) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return domain.E(domain.CodeUnavailable, "saved player entry is not an object")
	}
	out := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return domain.E(domain.CodeUnavailable, "invalid saved player field")
		}
		if _, exists := out[key]; exists {
			return domain.E(domain.CodeUnavailable, "duplicate saved player field")
		}
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return err
		}
		out[key] = value
	}
	if _, err = d.Token(); err != nil {
		return err
	}
	*fields = out
	return nil
}
