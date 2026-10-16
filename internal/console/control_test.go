package console

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

func TestFreshLegacyAcknowledgement(t *testing.T) {
	h, path := logFixture(t)
	_ = os.WriteFile(path, []byte("[12:00:00] [Server thread/INFO]: Done (1s)! For help, type \"help\"\n[12:00:01] [Server thread/INFO]: There are 0 of a max of 20 players online: \n"), 0600)
	cursor, err := newRawCursor(t.Context(), h.paths, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err = cursor.wait(ctx, ackList("1.21.9")); err == nil {
		t.Fatal("historical list acknowledged new command")
	}
	cursor, err = newRawCursor(t.Context(), h.paths, "test")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = f.WriteString("[12:00:02] [Server thread/INFO]: <Player> There are 0 of a max of 20 players online: \n[12:00:03] [Server thread/INFO]: There are 0 of a max of 20 players online: \n")
	f.Close()
	ctx2, cancel2 := context.WithTimeout(t.Context(), time.Second)
	defer cancel2()
	out, err := cursor.wait(ctx2, ackList("1.21.9"))
	if err != nil || strings.Contains(out, "<Player>") {
		t.Fatalf("fresh native response %q %v", out, err)
	}
}
func TestLegacySaveFlushAndRotationGap(t *testing.T) {
	h, path := logFixture(t)
	_ = os.WriteFile(path, []byte("old\n"), 0600)
	cursor, err := newRawCursor(t.Context(), h.paths, "test")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = f.WriteString("[12:00:00] [Server thread/INFO]: Flushing completed\n[12:00:00] [Server thread/INFO]: Saved the world\n")
	f.Close()
	out, err := cursor.wait(t.Context(), ackFlush("1.12.2"))
	if err != nil || !strings.Contains(out, "Saved the world") {
		t.Fatalf("legacy flush %q %v", out, err)
	}
	cursor, err = newRawCursor(t.Context(), h.paths, "test")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path)
	_ = os.WriteFile(path, []byte("[12:00:00] [Server thread/INFO]: Saved the game\n"), 0600)
	if _, err = cursor.wait(t.Context(), ackFlush("1.21.9")); err == nil {
		t.Fatal("lost cursor accepted acknowledgement")
	}
}

func rpcFixture(t *testing.T, protocol string, handler func(string, json.RawMessage) any) instance.ManagementConfig {
	t.Helper()
	return rpcFixtureBeforeResponse(t, protocol, handler, nil)
}

func rpcFixtureBeforeResponse(t *testing.T, protocol string, handler func(string, json.RawMessage) any, beforeResponse func(string)) instance.ManagementConfig {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 40) {
			t.Error("missing private auth")
			w.WriteHeader(401)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			var request struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      string          `json:"id"`
				Method  string          `json:"method"`
				Params  json.RawMessage `json:"params"`
			}
			if err = wsjson.Read(r.Context(), c, &request); err != nil {
				return
			}
			var result any
			if request.Method == "rpc.discover" {
				result = map[string]any{"openrpc": "1.3.2", "info": map[string]any{"version": protocol}, "methods": []any{
					map[string]any{"name": "minecraft:server/status", "result": map[string]any{"name": "status", "schema": map[string]any{"$ref": "#/components/schemas/server_state"}}},
					map[string]any{"name": "minecraft:gamerules", "result": map[string]any{"name": "gamerules", "schema": map[string]any{"type": "array"}}},
					map[string]any{"name": "minecraft:gamerules/update", "params": map[string]any{"name": "gamerule"}, "result": map[string]any{"name": "gamerule"}},
				}}
			} else {
				result = handler(request.Method, request.Params)
			}
			if beforeResponse != nil {
				beforeResponse(request.Method)
			}
			// The early heartbeat is deliberately unrelated to world readiness.
			_ = wsjson.Write(r.Context(), c, map[string]any{"jsonrpc": "2.0", "method": "minecraft:notification/server/status", "params": []any{map[string]any{"started": true}}})
			if err = wsjson.Write(r.Context(), c, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	host, port := splitTestAddress(t, srv.Listener.Addr().String())
	return instance.ManagementConfig{InstanceID: "test", Host: host, Port: port, Secret: strings.Repeat("a", 40), Protocol: protocol}
}
func TestMSMPReadinessAndExactRuleReadback(t *testing.T) {
	for _, protocol := range []string{"1.0.0", "2.0.0", "3.0.0", "3.1.0"} {
		t.Run(protocol, func(t *testing.T) {
			ready := false
			value := true
			rule := "minecraft:pvp"
			if protocol == "1.0.0" {
				rule = "pvp"
			}
			cfg := rpcFixture(t, protocol, func(method string, params json.RawMessage) any {
				switch method {
				case "minecraft:server/status":
					return map[string]any{"started": ready}
				case "minecraft:gamerules":
					var v any = value
					if protocol == "1.0.0" {
						if value {
							v = "true"
						} else {
							v = "false"
						}
					}
					return []any{map[string]any{"key": rule, "value": v, "type": "boolean"}}
				case "minecraft:gamerules/update":
					var p struct {
						Gamerule struct {
							Key   string          `json:"key"`
							Value json.RawMessage `json:"value"`
						} `json:"gamerule"`
					}
					if err := json.Unmarshal(params, &p); err != nil || p.Gamerule.Key != rule {
						t.Error("wrong rule payload")
					}
					if protocol == "1.0.0" {
						if string(p.Gamerule.Value) != `"false"` {
							t.Error("v1 requires string")
						}
					} else if string(p.Gamerule.Value) != "false" {
						t.Error("v2+ requires boolean")
					}
					value = false
					var v any = false
					if protocol == "1.0.0" {
						v = "false"
					}
					return map[string]any{"key": rule, "value": v, "type": "boolean"}
				}
				return nil
			})
			client, err := dialRPC(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			desired := []instance.DesiredRuntimeSetting{{Kind: "boolean_gamerule", Rule: rule, Value: false, Supported: true, Scope: "native_server_rules"}}
			state, err := client.verify(t.Context(), desired, true)
			if err != nil || state.ReadyForUse || state.GameReady {
				t.Fatalf("early heartbeat became ready: %+v %v", state, err)
			}
			ready = true
			state, err = client.verify(t.Context(), desired, true)
			if err != nil || !state.ReadyForUse || !state.RequiredSettingsVerified || value {
				t.Fatalf("native readback: %+v %v", state, err)
			}
		})
	}
}
func TestMSMPProtocolMismatchAndMissingMethods(t *testing.T) {
	cfg := rpcFixture(t, "9.0.0", func(string, json.RawMessage) any { return nil })
	cfg.Protocol = "3.1.0"
	if c, err := dialRPC(t.Context(), cfg); err == nil {
		c.Close()
		t.Fatal("accepted protocol mismatch")
	}
	cfg = rpcFixture(t, "3.1.0", func(method string, _ json.RawMessage) any {
		if method == "minecraft:server/status" {
			return map[string]any{"started": true}
		}
		return []any{map[string]any{"key": "minecraft:unrelated", "value": false, "type": "boolean"}}
	})
	c, err := dialRPC(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if state, err := c.verify(t.Context(), []instance.DesiredRuntimeSetting{{Kind: "boolean_gamerule", Rule: "minecraft:pvp", Supported: true, Scope: "native_server_rules"}}, true); err == nil || state.ReadyForUse {
		t.Fatal("missing rule accepted")
	}
}

func splitTestAddress(t *testing.T, address string) (string, int) {
	t.Helper()
	host, port, e := net.SplitHostPort(address)
	if e != nil {
		t.Fatal(e)
	}
	n, e := strconv.Atoi(port)
	if e != nil {
		t.Fatal(e)
	}
	return host, n
}

func TestMSMPRejectsMalformedOversizedAndTimedOutResponses(t *testing.T) {
	for _, name := range []string{"malformed", "oversized", "wrong_id", "wrong_version", "mixed_envelope", "null_result", "error_and_result", "notification_flood", "timeout"} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer c.CloseNow()
				var request struct {
					ID string `json:"id"`
				}
				if wsjson.Read(r.Context(), c, &request) != nil {
					return
				}
				switch name {
				case "malformed":
					_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"jsonrpc":`))
				case "oversized":
					_ = c.Write(r.Context(), websocket.MessageText, []byte(strings.Repeat("x", (1<<20)+1)))
				case "wrong_id":
					_ = wsjson.Write(r.Context(), c, map[string]any{"jsonrpc": "2.0", "id": "stale", "result": true})
				case "wrong_version":
					_ = wsjson.Write(r.Context(), c, map[string]any{"jsonrpc": "1.0", "id": request.ID, "result": true})
				case "mixed_envelope":
					_ = wsjson.Write(r.Context(), c, map[string]any{"jsonrpc": "2.0", "id": request.ID, "method": "minecraft:heartbeat", "result": true})
				case "null_result":
					_ = wsjson.Write(r.Context(), c, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": nil})
				case "error_and_result":
					_ = wsjson.Write(r.Context(), c, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": true, "error": nil})
				case "notification_flood":
					for i := 0; i < 65; i++ {
						if wsjson.Write(r.Context(), c, map[string]any{"jsonrpc": "2.0", "method": "minecraft:heartbeat"}) != nil {
							return
						}
					}
				case "timeout":
					// Await client closure under its deadline without an artificial sleep.
					_, _, _ = c.Read(r.Context())
				}
			}))
			defer srv.Close()
			host, port := splitTestAddress(t, srv.Listener.Addr().String())
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			client, err := dialRPC(ctx, instance.ManagementConfig{Host: host, Port: port, Secret: strings.Repeat("a", 40), Protocol: "3.1.0"})
			if err == nil {
				client.Close()
				t.Fatal("invalid native response accepted")
			}
			if strings.Contains(err.Error(), "Bearer") || strings.Contains(err.Error(), strings.Repeat("a", 40)) {
				t.Fatal("private authentication leaked")
			}
		})
	}
}
