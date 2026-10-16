package console

import (
	"context"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MapPosition contains only the three requested coordinates and dimension;
// inventories, complete NBT and raw command output never cross this boundary.
type MapPosition struct {
	Player     PlayerIdentity `json:"player"`
	X          float64        `json:"x"`
	Y          float64        `json:"y"`
	Z          float64        `json:"z"`
	Dimension  string         `json:"dimension"`
	ObservedAt time.Time      `json:"observed_at"`
	Source     string         `json:"source"`
}
type MapPlayers struct {
	Snapshot  PlayerSnapshot `json:"snapshot"`
	Positions []MapPosition  `json:"positions"`
	Available bool           `json:"available"`
	Truncated bool           `json:"truncated"`
	Gap       bool           `json:"gap"`
	Reason    string         `json:"reason"`
}

var mapNumber = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?[dD]?$`)
var mapDimensionID = regexp.MustCompile(`^[a-z0-9_.-]+:[a-z0-9_./-]+$`)

func mapCoordinates(raw string) ([3]float64, error) {
	out := [3]float64{}
	if len(raw) > 256 || !strings.HasPrefix(raw, "[") || !strings.HasSuffix(raw, "]") {
		return out, domain.E(domain.CodeUnavailable, "Native position shape is unknown.")
	}
	parts := strings.Split(raw[1:len(raw)-1], ",")
	if len(parts) != 3 {
		return out, domain.E(domain.CodeUnavailable, "Native position has no exact three coordinates.")
	}
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if !mapNumber.MatchString(p) {
			return out, domain.E(domain.CodeUnavailable, "Native position is not finite numeric evidence.")
		}
		v, e := strconv.ParseFloat(strings.TrimRight(p, "dD"), 64)
		limit := 30000000.0
		if i == 1 {
			limit = 2032
		}
		if e != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < -limit || v >= limit {
			return out, domain.E(domain.CodeUnavailable, "Native position exceeds map coordinate limits.")
		}
		out[i] = v
	}
	return out, nil
}
func mapDimension(raw string) (string, error) {
	// Only exact namespaced strings are accepted; legacy integer IDs and unknown
	// serialization are not guessed or associated with a custom dimension.
	if len(raw) < 3 || len(raw) > 1026 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", domain.E(domain.CodeUnavailable, "Native dimension is not an exact namespaced string.")
	}
	d := raw[1 : len(raw)-1]
	if !mapDimensionID.MatchString(d) || strings.Contains(d, "..") || strings.Contains(d, "//") {
		return "", domain.E(domain.CodeUnavailable, "Native dimension identity is unsafe or unknown.")
	}
	return d, nil
}
func mapPositionVersion(version string) bool { return version == "1.21.9" || version == "26.3" }
func readMapPosition(ctx context.Context, scope *sessionScope, p PlayerIdentity) (MapPosition, error) {
	out := MapPosition{Player: p, Source: "fresh_native_targeted_data_get"}
	if !playerName.MatchString(p.Name) {
		return out, domain.E(domain.CodeUnavailable, "Targeted native positions require an observed exact username.")
	}
	target := p.Name
	if p.UUID != "" {
		if !playerUUID.MatchString(p.UUID) {
			return out, domain.E(domain.CodeUnavailable, "Native target UUID is invalid.")
		}
		target = p.UUID
	}
	query := func(field string) (string, error) {
		prefix := p.Name + " has the following entity data: "
		value := ""
		_, e := scope.legacy(ctx, "data get entity "+target+" "+field, func(line string) bool {
			if !strings.HasPrefix(line, prefix) || len(line) > 512 {
				return false
			}
			candidate := strings.TrimPrefix(line, prefix)
			if field == "Pos" {
				if _, e := mapCoordinates(candidate); e != nil {
					return false
				}
			} else {
				if _, e := mapDimension(candidate); e != nil {
					return false
				}
			}
			value = candidate
			return true
		})
		return value, e
	}
	before, e := query("Dimension")
	if e != nil {
		return out, e
	}
	coords, e := query("Pos")
	if e != nil {
		return out, e
	}
	after, e := query("Dimension")
	if e != nil {
		return out, e
	}
	if before != after {
		return out, domain.E(domain.CodeUnavailable, "Player dimension changed during position observation.")
	}
	out.Dimension, e = mapDimension(before)
	if e != nil {
		return out, e
	}
	xyz, e := mapCoordinates(coords)
	if e != nil {
		return out, e
	}
	if e = scope.check(ctx); e != nil {
		return out, e
	}
	out.X, out.Y, out.Z = xyz[0], xyz[1], xyz[2]
	out.ObservedAt = time.Now().UTC()
	return out, nil
}

// PlayerPositions uses one original read-only reservation and captured JVM
// generation. Bounded callers share these observations at the mapview layer.
func (s *Service) PlayerPositions(ctx context.Context, id string) (MapPlayers, error) {
	out := MapPlayers{Positions: []MapPosition{}, Gap: true, Reason: "Native position observation unavailable."}
	if s.queue == nil {
		return out, domain.E(domain.CodeUnavailable, "Native map admission unavailable.")
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	e := s.queue.WithIdle(ctx, id, func() error {
		return s.withSession(ctx, id, func(session *Session) error {
			snap, e := session.PlayerSnapshot(ctx)
			if e != nil {
				return e
			}
			out.Snapshot = snap
			if len(snap.Players) > 16 {
				out.Snapshot.Players = append([]PlayerIdentity{}, snap.Players[:16]...)
				out.Snapshot.Complete = false
				out.Truncated = true
			}
			scope, release, e := session.enter(ctx, false)
			if e != nil {
				return e
			}
			defer release()
			stop := context.AfterFunc(scope.ctx, cancel)
			defer stop()
			if !mapPositionVersion(scope.detail.Instance.MinecraftVersion) {
				out.Reason = "Targeted native position fields are statically verified only for 1.21.9 and 26.3. Minecraft 1.12.2 has no data-get command; other versions remain unverified. Presence is still observed."
				return nil
			}
			out.Available = true
			out.Gap = false
			out.Reason = "Targeted native coordinates are separate observations, not a coherent live snapshot. Dimension is checked before and after Pos; same-dimension motion and round-trip teleports may occur between commands."
			limit := len(snap.Players)
			if limit > 16 {
				limit = 16
				out.Truncated = true
				out.Gap = true
				out.Reason += " Coverage capped at the first 16 observed players."
			}
			for _, p := range snap.Players[:limit] {
				position, e := readMapPosition(ctx, scope, p)
				if e != nil {
					out.Gap = true
					out.Reason += " One or more positions could not be verified."
					break
				}
				out.Positions = append(out.Positions, position)
			}
			if e = scope.check(ctx); e != nil {
				out.Positions = []MapPosition{}
				out.Gap = true
				return e
			}
			return nil
		})
	})
	if e != nil {
		out.Positions = []MapPosition{}
		out.Gap = true
		out.Reason = "Native roster/position observation is busy, changed generation, or unavailable; current presence is unknown."
		out.Snapshot = PlayerSnapshot{InstanceID: id, Players: []PlayerIdentity{}, Gap: true, Reason: out.Reason}
		return out, fmt.Errorf("observe native map players: %w", e)
	}
	return out, nil
}
