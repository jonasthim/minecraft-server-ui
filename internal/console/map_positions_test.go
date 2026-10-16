package console

import (
	"encoding/json"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"os"
	"strings"
	"testing"
)

func TestPositionStrictNarrowDecoding(t *testing.T) {
	for _, raw := range []string{"[1.5d, -64.0d, 2.0d]", "[1.5, -64.0, 2e2]"} {
		if _, e := mapCoordinates(raw); e != nil {
			t.Fatal(raw, e)
		}
	}
	for _, raw := range []string{"[NaNd, 0d, 0d]", "[1e999d, 0d, 0d]", "[30000000d, 0d, 0d]", "[1d, 2d]", "{Inventory:[]}", "[0d, 9000d, 0d]"} {
		if _, e := mapCoordinates(raw); e == nil {
			t.Fatal("accepted", raw)
		}
	}
	if d, e := mapDimension(`"example:custom/path"`); e != nil || d != "example:custom/path" {
		t.Fatal(d, e)
	}
	for _, raw := range []string{`0`, `"../secret"`, `"minecraft:overworld" trailing`, `"minecraft:foo/../bar"`} {
		if _, e := mapDimension(raw); e == nil {
			t.Fatal("unsafe dimension", raw)
		}
	}
}
func TestPositionQueriesAreNarrowAndRejectDimensionChange(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "stable", true: "teleport"}[changed], func(t *testing.T) {
			s, n, path := serviceFixture(t)
			s.instances = fixtureInstances{detail: instance.Detail{Instance: domain.Instance{ID: "test", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21.9"}}}
			// This fixture has no authenticated management endpoint, so its roster is
			// supplied through the typed projection helper under the real session scope.
			commands := []string{}
			dims := 0
			n.write = func(command string) error {
				commands = append(commands, command)
				value := ""
				if command == "data get entity Alex Dimension" {
					dims++
					value = `"minecraft:overworld"`
					if changed && dims == 2 {
						value = `"minecraft:the_nether"`
					}
				} else if command == "data get entity Alex Pos" {
					value = "[1.5d, 64.0d, -2.5d]"
				} else {
					t.Fatal("unexpected command", command)
				}
				f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
				if e != nil {
					return e
				}
				defer f.Close()
				_, e = f.WriteString("[12:00:00] [Server thread/INFO]: Alex has the following entity data: " + value + "\n")
				return e
			}
			e := s.withSession(t.Context(), "test", func(session *Session) error {
				scope, release, e := session.enter(t.Context(), false)
				if e != nil {
					return e
				}
				defer release()
				p, e := readMapPosition(t.Context(), scope, PlayerIdentity{Name: "Alex", Authority: "name_only", Source: "fresh_native_console_list"})
				if changed {
					if e == nil {
						t.Fatal("teleport marker accepted", p)
					}
				} else if e != nil || p.X != 1.5 || p.Z != -2.5 || p.Player.UUID != "" {
					t.Fatal(p, e)
				}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			if strings.Join(commands, "|") != "data get entity Alex Dimension|data get entity Alex Pos|data get entity Alex Dimension" {
				t.Fatal(commands)
			}
		})
	}
}

func TestNativeMapPositionsFullReservationAndGenerationFence(t *testing.T) {
	for _, scenario := range []string{"ready", "generation", "coverage"} {
		t.Run(scenario, func(t *testing.T) {
			s, n, path := serviceFixture(t)
			roster := []map[string]string{{"name": "Alex", "id": "00000000-0000-0000-0000-000000000001"}}
			if scenario == "coverage" {
				for i := 1; i < 20; i++ {
					roster = append(roster, map[string]string{"name": fmt.Sprintf("Player%d", i)})
				}
			}
			cfg := playerRPCFixture(t, "1.0.0", false, func(string, json.RawMessage) any { return roster }, nil)
			s.instances = managedFixtureInstances{fixtureInstances: fixtureInstances{detail: instance.Detail{Instance: domain.Instance{ID: "test", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21.9"}}}, cfg: cfg}
			calls := 0
			n.write = func(command string) error {
				calls++
				parts := strings.Fields(command)
				if len(parts) != 5 || parts[0] != "data" || parts[1] != "get" || parts[2] != "entity" || (parts[4] != "Pos" && parts[4] != "Dimension") {
					t.Fatal(command)
				}
				displayName := parts[3]
				if displayName == roster[0]["id"] {
					displayName = "Alex"
				}
				value := `"minecraft:overworld"`
				if parts[4] == "Pos" {
					value = "[1.0d, 64.0d, 2.0d]"
				}
				f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
				if e != nil {
					return e
				}
				defer f.Close()
				_, e = f.WriteString("[12:00:00] [Server thread/INFO]: " + displayName + " has the following entity data: " + value + "\n")
				if scenario == "generation" && calls == 2 {
					n.mu.Lock()
					n.identity.StartIdentity = "replacement"
					n.mu.Unlock()
				}
				return e
			}
			got, e := s.PlayerPositions(t.Context(), "test")
			if scenario == "generation" {
				if len(got.Positions) != 0 || !got.Gap {
					t.Fatal("stale JVM marker exposed", got, e)
				}
				return
			}
			if e != nil || !got.Snapshot.Known || !got.Available || len(got.Positions) == 0 {
				t.Fatal(got, e)
			}
			if got.Positions[0].Player.UUID != roster[0]["id"] || got.Positions[0].Dimension != "minecraft:overworld" {
				t.Fatal(got)
			}
			if scenario == "coverage" && (calls != 48 || !got.Truncated || !got.Gap || len(got.Positions) != 16 || len(got.Snapshot.Players) != 16 || got.Snapshot.Count != 20) {
				t.Fatal("unbounded/false coverage", calls, got)
			}
		})
	}
}
