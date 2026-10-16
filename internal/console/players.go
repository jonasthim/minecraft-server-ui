package console

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

// PlayerIdentity records server evidence, never a UUID inferred from a name.
// UUID authority is instance-local: it does not assert Mojang authentication.
type PlayerIdentity struct {
	UUID      string `json:"uuid,omitempty"`
	Name      string `json:"name,omitempty"`
	Authority string `json:"authority"`
	Source    string `json:"source"`
}
type PlayerSnapshot struct {
	InstanceID string           `json:"instance_id"`
	Generation string           `json:"generation"`
	ObservedAt time.Time        `json:"observed_at"`
	Known      bool             `json:"known"`
	Complete   bool             `json:"complete"`
	Count      int              `json:"count"`
	Players    []PlayerIdentity `json:"players"`
	Gap        bool             `json:"gap"`
	Reason     string           `json:"reason"`
}
type PlayerMutation struct {
	Action          string `json:"action"`
	UUID            string `json:"uuid,omitempty"`
	Name            string `json:"name,omitempty"`
	PermissionLevel *int   `json:"permission_level,omitempty"`
}

// PlayerMutationResult separates delivery, acknowledgement, and independent
// native readback. Legacy saved-file readback belongs to the players job.
type PlayerMutationResult struct {
	Received         bool
	Acknowledged     bool
	ReadbackVerified bool
	Output           string
}
type PlayerListEntry struct {
	Player              PlayerIdentity `json:"player"`
	PermissionLevel     *int           `json:"permission_level,omitempty"`
	BypassesPlayerLimit *bool          `json:"bypasses_player_limit,omitempty"`
}
type PlayerList struct {
	Kind            string            `json:"kind"`
	Entries         []PlayerListEntry `json:"entries"`
	Source          string            `json:"source"`
	ObservedAt      time.Time         `json:"observed_at"`
	RuntimeVerified bool              `json:"runtime_verified"`
}

var playerName = regexp.MustCompile(`^[A-Za-z0-9_]{1,16}$`)
var playerUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func ValidatePlayerIdentity(uuid, name string) error {
	if uuid == "" && name == "" || uuid != "" && !playerUUID.MatchString(uuid) || name != "" && !playerName.MatchString(name) {
		return domain.E(domain.CodeValidationFailed, "invalid player UUID or username")
	}
	return nil
}
func ValidatePlayerMutation(m PlayerMutation) error {
	if err := ValidatePlayerIdentity(m.UUID, m.Name); err != nil {
		return err
	}
	switch m.Action {
	case "kick", "whitelist_add", "whitelist_remove", "ban", "unban", "op", "deop":
	default:
		return domain.E(domain.CodeValidationFailed, "invalid player operation")
	}
	if m.PermissionLevel != nil && (m.Action != "op" || *m.PermissionLevel < 1 || *m.PermissionLevel > 4) {
		return domain.E(domain.CodeValidationFailed, "permission level requires op and a level from 1 to 4")
	}
	return nil
}
func decodePlayerIdentities(raw []byte) ([]PlayerIdentity, error) {
	var wire []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire == nil || len(wire) > 10000 {
		return nil, domain.E(domain.CodeUnavailable, "native player identities are invalid or exceed limits")
	}
	out := make([]PlayerIdentity, 0, len(wire))
	ids, names := map[string]bool{}, map[string]bool{}
	for _, p := range wire {
		if ValidatePlayerIdentity(p.ID, p.Name) != nil || p.ID != "" && ids[p.ID] || p.Name != "" && names[strings.ToLower(p.Name)] {
			return nil, domain.E(domain.CodeUnavailable, "native player identities are invalid or ambiguous")
		}
		ids[p.ID] = true
		names[strings.ToLower(p.Name)] = true
		authority := "name_only"
		if p.ID != "" {
			authority = "server_uuid"
		}
		out = append(out, PlayerIdentity{p.ID, p.Name, authority, "authenticated_msmp"})
	}
	return out, nil
}
func (s *Service) PlayerSnapshot(ctx context.Context, id string) (PlayerSnapshot, error) {
	var out PlayerSnapshot
	if s.queue == nil {
		return out, domain.E(domain.CodeUnavailable, "player observation admission is unavailable")
	}
	err := s.queue.WithIdle(ctx, id, func() error {
		return s.withSession(ctx, id, func(session *Session) error { var e error; out, e = session.PlayerSnapshot(ctx); return e })
	})
	return out, err
}
func (s *Session) PlayerSnapshot(ctx context.Context) (PlayerSnapshot, error) {
	scope, release, err := s.enter(ctx, false)
	if err != nil {
		return PlayerSnapshot{}, err
	}
	defer release()
	operation, cancel := commandTimeout(ctx)
	defer cancel()
	stop := context.AfterFunc(scope.ctx, cancel)
	defer stop()
	out := PlayerSnapshot{InstanceID: scope.identity.InstanceID, Generation: fmt.Sprintf("%d:%s", scope.identity.PID, scope.identity.StartIdentity), Players: []PlayerIdentity{}, ObservedAt: time.Now().UTC()}
	if _, e := instance.ManagementProtocol(scope.detail.Instance.MinecraftVersion); e == nil {
		c, e := scope.rpc(operation)
		if e != nil {
			return out, e
		}
		defer c.Close()
		if !c.has("minecraft:players", "", "players") {
			return out, domain.E(domain.CodeUnsupported, "native player listing is unavailable")
		}
		raw, e := c.call(operation, "minecraft:players", nil)
		if e != nil {
			return out, e
		}
		out.Players, e = decodePlayerIdentities(raw)
		if e != nil {
			return out, e
		}
		out.Count = len(out.Players)
	} else {
		count := -1
		roster := ""
		old := strings.HasPrefix(scope.detail.Instance.MinecraftVersion, "1.12.")
		oracle := func(message string) bool {
			if count < 0 {
				if !ackList(scope.detail.Instance.MinecraftVersion)(message) {
					return false
				}
				count, _ = strconv.Atoi(strings.Fields(strings.TrimPrefix(message, "There are "))[0])
				if old {
					count, _ = strconv.Atoi(strings.Split(strings.TrimPrefix(message, "There are "), "/")[0])
					return count == 0
				}
				roster = strings.SplitN(message, ": ", 2)[1]
				return true
			}
			if old {
				roster = message
				return true
			}
			return false
		}
		if _, e := scope.legacy(operation, "list", oracle); e != nil {
			return out, e
		}
		if count < 0 || count > 10000 {
			return out, domain.E(domain.CodeUnavailable, "native player count exceeds limits")
		}
		if roster != "" {
			seen := map[string]bool{}
			for _, name := range strings.Split(roster, ", ") {
				if !playerName.MatchString(name) || seen[strings.ToLower(name)] {
					return out, domain.E(domain.CodeUnavailable, "legacy player roster is malformed or ambiguous")
				}
				seen[strings.ToLower(name)] = true
				out.Players = append(out.Players, PlayerIdentity{Name: name, Authority: "name_only", Source: "fresh_native_console_list"})
			}
		}
		if count != len(out.Players) {
			return out, domain.E(domain.CodeUnavailable, "legacy player count and roster disagree")
		}
		out.Count = count
	}
	if err = scope.check(operation); err != nil {
		return out, err
	}
	out.Known = true
	out.Complete = true
	out.ObservedAt = time.Now().UTC()
	out.Reason = "Observed roster; changes between samples are unknown. UUID evidence is instance-local and does not prove account authentication."
	return out, nil
}
func listMethod(kind string) (string, string, error) {
	switch kind {
	case "whitelist":
		return "allowlist", "allowlist", nil
	case "bans":
		return "bans", "banlist", nil
	case "operators":
		return "operators", "operators", nil
	}
	return "", "", domain.E(domain.CodeValidationFailed, "invalid player list")
}
func (s *Service) NativePlayerList(ctx context.Context, id, kind string) (PlayerList, error) {
	var out PlayerList
	if s.queue == nil {
		return out, domain.E(domain.CodeUnavailable, "player list admission unavailable")
	}
	err := s.queue.WithIdle(ctx, id, func() error {
		return s.withSession(ctx, id, func(session *Session) error { var e error; out, e = session.PlayerList(ctx, kind); return e })
	})
	return out, err
}
func (c *rpcClient) playerList(ctx context.Context, kind string) (PlayerList, error) {
	out := PlayerList{Kind: kind, Entries: []PlayerListEntry{}, Source: "authenticated_msmp", ObservedAt: time.Now().UTC()}
	method, result, err := listMethod(kind)
	if err != nil {
		return out, err
	}
	if !c.has("minecraft:"+method, "", result) {
		return out, domain.E(domain.CodeUnsupported, "native player list method unavailable")
	}
	raw, err := c.call(ctx, "minecraft:"+method, nil)
	if err != nil {
		return out, err
	}
	return decodeNativePlayerList(raw, out)
}
func decodeNativePlayerList(raw []byte, out PlayerList) (PlayerList, error) {
	kind := out.Kind
	if kind == "whitelist" {
		ps, e := decodePlayerIdentities(raw)
		if e != nil {
			return out, e
		}
		for _, p := range ps {
			out.Entries = append(out.Entries, PlayerListEntry{Player: p})
		}
	} else {
		var wire []struct {
			Player          json.RawMessage `json:"player"`
			PermissionLevel *int            `json:"permissionLevel"`
			Bypasses        *bool           `json:"bypassesPlayerLimit"`
		}
		if json.Unmarshal(raw, &wire) != nil || wire == nil || len(wire) > 10000 {
			return out, domain.E(domain.CodeUnavailable, "native player list is invalid")
		}
		identities := []json.RawMessage{}
		for _, p := range wire {
			identities = append(identities, p.Player)
		}
		b, _ := json.Marshal(identities)
		ps, e := decodePlayerIdentities(b)
		if e != nil {
			return out, e
		}
		for i, p := range ps {
			if kind == "operators" && (wire[i].PermissionLevel == nil || *wire[i].PermissionLevel < 0 || *wire[i].PermissionLevel > 4) {
				return out, domain.E(domain.CodeUnavailable, "native operator permission level invalid")
			}
			out.Entries = append(out.Entries, PlayerListEntry{p, wire[i].PermissionLevel, wire[i].Bypasses})
		}
	}
	out.RuntimeVerified = true
	return out, nil
}
func (s *Session) PlayerList(ctx context.Context, kind string) (PlayerList, error) {
	scope, release, err := s.enter(ctx, false)
	if err != nil {
		return PlayerList{}, err
	}
	defer release()
	operation, cancel := commandTimeout(ctx)
	defer cancel()
	stop := context.AfterFunc(scope.ctx, cancel)
	defer stop()
	if _, err = instance.ManagementProtocol(scope.detail.Instance.MinecraftVersion); err != nil {
		return PlayerList{}, domain.E(domain.CodeUnsupported, "legacy lists require a saved-list snapshot")
	}
	c, err := scope.rpc(operation)
	if err != nil {
		return PlayerList{}, err
	}
	defer c.Close()
	return c.playerList(operation, kind)
}

// MutatePlayer is privileged only through an already authorized current job.
// Legacy results acknowledge command execution; callers must verify saved list
// membership. Unsupported explicit legacy permission levels fail before delivery.
func (s *Session) MutatePlayer(ctx context.Context, m PlayerMutation) (PlayerMutationResult, error) {
	result := PlayerMutationResult{}
	if err := ValidatePlayerMutation(m); err != nil {
		return result, err
	}
	scope, release, err := s.enter(ctx, false)
	if err != nil {
		return result, err
	}
	defer release()
	operation, cancel := commandTimeout(ctx)
	defer cancel()
	stop := context.AfterFunc(scope.ctx, cancel)
	defer stop()
	if _, e := instance.ManagementProtocol(scope.detail.Instance.MinecraftVersion); e != nil {
		if m.Name == "" || m.UUID != "" || m.PermissionLevel != nil {
			return result, domain.E(domain.CodeUnsupported, "legacy online mutation requires a username only and server default permission level")
		}
		command, ack, err := legacyPlayerMutation(scope.detail.Instance.MinecraftVersion, m)
		if err != nil {
			return result, err
		}
		legacy, e := scope.legacy(operation, command, ack)
		return PlayerMutationResult{Received: legacy.Received, Acknowledged: legacy.Verified, Output: legacy.Output}, e
	}
	if m.UUID == "" {
		return result, domain.E(domain.CodeValidationFailed, "online management mutation requires a proven instance-local UUID")
	}
	c, err := scope.rpc(operation)
	if err != nil {
		return result, err
	}
	defer c.Close()
	p := map[string]string{"id": m.UUID}
	kind, param, verb := "", "", ""
	var item any = p
	switch m.Action {
	case "kick":
		kind = "players"
		verb = "kick"
		param = "kick"
		item = map[string]any{"player": p}
	case "whitelist_add":
		kind = "allowlist"
		verb = "add"
		param = "add"
	case "whitelist_remove":
		kind = "allowlist"
		verb = "remove"
		param = "remove"
	case "ban":
		kind = "bans"
		verb = "add"
		param = "add"
		item = map[string]any{"player": p}
	case "unban":
		kind = "bans"
		verb = "remove"
		param = "remove"
	case "op":
		kind = "operators"
		verb = "add"
		param = "add"
		entry := map[string]any{"player": p}
		if m.PermissionLevel != nil {
			entry["permissionLevel"] = *m.PermissionLevel
		}
		item = entry
	case "deop":
		kind = "operators"
		verb = "remove"
		param = "remove"
	}
	expected := kind
	if kind == "bans" {
		expected = "banlist"
	}
	if m.Action == "kick" {
		expected = "kicked"
	}
	if !c.has("minecraft:"+kind+"/"+verb, param, expected) {
		return result, domain.E(domain.CodeUnsupported, "native player mutation method unavailable")
	}
	readMethod := kind
	readResult := expected
	if m.Action == "kick" {
		readResult = "players"
	}
	if !c.has("minecraft:"+readMethod, "", readResult) {
		return result, domain.E(domain.CodeUnsupported, "native player readback method unavailable")
	}
	raw, err := c.call(operation, "minecraft:"+kind+"/"+verb, map[string]any{param: []any{item}})
	result.Received = true
	if err != nil {
		return result, err
	}
	if m.Action == "kick" {
		ps, e := decodePlayerIdentities(raw)
		if e != nil {
			return result, e
		}
		if len(ps) != 1 || ps[0].UUID != m.UUID {
			return result, domain.E(domain.CodeUnavailable, "kick acknowledgement did not identify the requested player")
		}
		result.Acknowledged = true
		raw, e = c.call(operation, "minecraft:players", nil)
		if e != nil {
			return result, e
		}
		ps, e = decodePlayerIdentities(raw)
		if e != nil {
			return result, e
		}
		for _, p := range ps {
			if p.UUID == m.UUID {
				return result, domain.E(domain.CodeUnavailable, "kick delivered but player remains in fresh roster")
			}
		}
	} else {
		listKind := map[string]string{"allowlist": "whitelist", "bans": "bans", "operators": "operators"}[kind]
		acknowledged, e := decodeNativePlayerList(raw, PlayerList{Kind: listKind, Entries: []PlayerListEntry{}, Source: "authenticated_msmp", ObservedAt: time.Now().UTC()})
		if e != nil {
			return result, e
		}
		if err = verifyNativePlayerMembership(acknowledged, m, verb == "add"); err != nil {
			return result, err
		}
		result.Acknowledged = true
		// Mutation acknowledgement is followed by an independent native readback.
		list, e := c.playerList(operation, listKind)
		if e != nil {
			return result, e
		}
		if err = verifyNativePlayerMembership(list, m, verb == "add"); err != nil {
			return result, err
		}
	}
	result.Acknowledged = true
	result.ReadbackVerified = true
	result.Output = "Native player operation acknowledged and read back."
	return result, nil
}
func legacyPlayerMutation(version string, m PlayerMutation) (string, acknowledgement, error) {
	old := strings.HasPrefix(version, "1.12.")
	name := m.Name
	command, reply := "", ""
	switch m.Action {
	case "whitelist_add":
		command = "whitelist add " + name
		reply = "Added " + name + " to the whitelist"
	case "whitelist_remove":
		command = "whitelist remove " + name
		reply = "Removed " + name + " from the whitelist"
	case "op":
		command = "op " + name
		reply = "Made " + name + " a server operator"
		if old {
			reply = "Opped " + name
		}
	case "deop":
		command = "deop " + name
		reply = "Made " + name + " no longer a server operator"
		if old {
			reply = "De-opped " + name
		}
	case "ban":
		command = "ban " + name
		reply = "Banned " + name + ": Banned by an operator."
		if old {
			reply = "Banned player " + name
		}
	case "unban":
		command = "pardon " + name
		reply = "Unbanned " + name
		if old {
			reply = "Unbanned player " + name
		}
	case "kick":
		command = "kick " + name
		reply = "Kicked " + name + ": Kicked by an operator"
		if old {
			reply = "Kicked " + name + " from the game"
		}
	default:
		return "", nil, domain.E(domain.CodeValidationFailed, "invalid player operation")
	}
	return command, func(s string) bool { return s == reply }, nil
}

func verifyNativePlayerMembership(list PlayerList, m PlayerMutation, want bool) error {
	found := false
	for _, entry := range list.Entries {
		if entry.Player.UUID == m.UUID {
			found = true
			if m.Name != "" && entry.Player.Name != "" && !strings.EqualFold(entry.Player.Name, m.Name) {
				return domain.E(domain.CodeUnavailable, "native UUID and username readback disagree")
			}
			if m.Action == "op" && m.PermissionLevel != nil && (entry.PermissionLevel == nil || *entry.PermissionLevel != *m.PermissionLevel) {
				return domain.E(domain.CodeUnavailable, "native operator permission readback differs")
			}
		}
	}
	if found != want {
		return domain.E(domain.CodeUnavailable, "player mutation acknowledgement or independent readback differs")
	}
	return nil
}
