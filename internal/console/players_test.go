package console

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestPlayerIdentityValidation(t *testing.T) {
	for _, raw := range []string{`[{"id":"00000000-0000-0000-0000-000000000001","name":"Alex"}]`, `[{"id":"00000000-0000-0000-0000-000000000001"}]`, `[{"name":"Alex"}]`, `[]`} {
		if _, err := decodePlayerIdentities([]byte(raw)); err != nil {
			t.Fatalf("valid %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`null`, `[{}]`, `[{"id":"bad","name":"Alex"}]`, `[{"name":"@a"}]`, `[{"name":"Alex"},{"name":"alex"}]`, `[{"id":"00000000-0000-0000-0000-000000000001"},{"id":"00000000-0000-0000-0000-000000000001"}]`} {
		if _, err := decodePlayerIdentities([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestPlayerSnapshotLegacyFreshNamesAndSessionExpiry(t *testing.T) {
	s, n, path := serviceFixture(t)
	n.write = func(command string) error {
		f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		defer f.Close()
		_, e = f.WriteString("[12:00:00] [Server thread/INFO]: There are 2 of a max of 20 players online: Alex, Steve\n")
		return e
	}
	snap, err := s.PlayerSnapshot(t.Context(), "test")
	if err != nil || !snap.Known || len(snap.Players) != 2 || snap.Players[0].UUID != "" || snap.Players[0].Authority != "name_only" {
		t.Fatalf("snapshot %+v %v", snap, err)
	}
	var retained *Session
	if err = s.withSession(t.Context(), "test", func(session *Session) error { retained = session; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = retained.PlayerSnapshot(context.Background()); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal("expired player scope", err)
	}
}

func TestLegacyPlayerSnapshot112PairedRoster(t *testing.T) {
	s, n, path := serviceFixture(t)
	s.instances = fixtureInstances{detail: instance.Detail{Instance: domain.Instance{ID: "test", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.12.2"}}}
	n.write = func(command string) error {
		f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		defer f.Close()
		_, e = f.WriteString("[12:00:00] [Server thread/INFO]: There are 2/20 players online:\n[12:00:00] [Server thread/INFO]: Alex, Steve\n")
		return e
	}
	snap, err := s.PlayerSnapshot(t.Context(), "test")
	if err != nil || len(snap.Players) != 2 || snap.Count != 2 {
		t.Fatalf("paired legacy %+v %v", snap, err)
	}
}
func playerRPCFixture(t *testing.T, protocol string, omitRead bool, handler func(string, json.RawMessage) any, before func(string)) instance.ManagementConfig {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 40) {
			w.WriteHeader(401)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			var req struct {
				ID     string          `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if wsjson.Read(r.Context(), c, &req) != nil {
				return
			}
			var result any
			if req.Method == "rpc.discover" {
				methods := []any{}
				for _, m := range []struct{ name, param, result string }{{"players", "", "players"}, {"players/kick", "kick", "kicked"}, {"allowlist", "", "allowlist"}, {"allowlist/add", "add", "allowlist"}, {"allowlist/remove", "remove", "allowlist"}, {"bans", "", "banlist"}, {"bans/add", "add", "banlist"}, {"bans/remove", "remove", "banlist"}, {"operators", "", "operators"}, {"operators/add", "add", "operators"}, {"operators/remove", "remove", "operators"}} {
					if omitRead && m.name == "allowlist" {
						continue
					}
					item := map[string]any{"name": "minecraft:" + m.name, "result": map[string]any{"name": m.result}}
					if m.param != "" {
						item["params"] = map[string]any{"name": m.param}
					}
					methods = append(methods, item)
				}
				result = map[string]any{"openrpc": "1.3.2", "info": map[string]any{"version": protocol}, "methods": methods}
			} else {
				result = handler(req.Method, req.Params)
			}
			if before != nil {
				before(req.Method)
			}
			if wsjson.Write(r.Context(), c, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}) != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	n, _ := strconv.Atoi(port)
	return instance.ManagementConfig{Host: "127.0.0.1", Port: n, Secret: strings.Repeat("a", 40), Protocol: protocol}
}
func TestNativePlayerMutationRequiresFreshExactReadback(t *testing.T) {
	for _, protocol := range []string{"1.0.0", "3.1.0"} {
		for _, scenario := range []string{"success", "mismatch", "malformed_ack", "missing_read", "stale"} {
			t.Run(protocol+scenario, func(t *testing.T) {
				s, n, _ := serviceFixture(t)
				mutated := false
				uuid := "00000000-0000-0000-0000-000000000001"
				entry := []map[string]string{{"id": uuid, "name": "Alex"}}
				cfg := playerRPCFixture(t, protocol, scenario == "missing_read", func(method string, params json.RawMessage) any {
					if method == "minecraft:allowlist/add" {
						mutated = true
						if scenario == "malformed_ack" {
							return true
						}
						return entry
					}
					if scenario == "mismatch" {
						return []any{}
					}
					return entry
				}, func(method string) {
					if scenario == "stale" && method == "minecraft:allowlist/add" {
						n.mu.Lock()
						n.identity.StartIdentity = "replacement"
						n.mu.Unlock()
					}
				})
				version := "1.21.9"
				if protocol == "3.1.0" {
					version = "26.3"
				}
				s.instances = managedFixtureInstances{fixtureInstances: fixtureInstances{detail: instance.Detail{Instance: domain.Instance{ID: "test", Flavor: domain.FlavorVanilla, MinecraftVersion: version}}}, cfg: cfg}
				var result PlayerMutationResult
				err := s.withSession(t.Context(), "test", func(session *Session) error {
					var e error
					result, e = session.MutatePlayer(t.Context(), PlayerMutation{Action: "whitelist_add", UUID: uuid, Name: "Alex"})
					return e
				})
				if scenario == "success" {
					if err != nil || !result.ReadbackVerified {
						t.Fatalf("success %+v %v", result, err)
					}
				} else if err == nil || result.ReadbackVerified {
					t.Fatalf("false success %+v %v", result, err)
				}
				if scenario == "missing_read" && mutated {
					t.Fatal("mutated without discovered readback capability")
				}
			})
		}
	}
}
