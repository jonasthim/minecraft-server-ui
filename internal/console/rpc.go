package console

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

type State struct {
	LogsAvailable            bool      `json:"logs_available"`
	LogsReason               string    `json:"logs_reason"`
	LogsObservedAt           time.Time `json:"logs_observed_at"`
	ProcessRunning           bool      `json:"process_running"`
	ControlConnected         bool      `json:"control_connected"`
	GameReady                bool      `json:"game_ready"`
	RequiredSettingsVerified bool      `json:"required_settings_verified"`
	ReadyForUse              bool      `json:"ready_for_use"`
	Status                   string    `json:"status"`
	Reason                   string    `json:"reason"`
	Source                   string    `json:"source"`
	RuleScope                string    `json:"rule_scope"`
	GameplayEffect           string    `json:"gameplay_effect"`
	ObservedAt               time.Time `json:"observed_at"`
}
type rpcMethod struct {
	Name   string `json:"name"`
	Params *struct {
		Name string `json:"name"`
	} `json:"params"`
	Result *struct {
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
	} `json:"result"`
}
type rpcClient struct {
	conn     *websocket.Conn
	protocol string
	methods  map[string]rpcMethod
	check    func(context.Context) error
}

func dialRPC(ctx context.Context, cfg instance.ManagementConfig) (*rpcClient, error) {
	return dialRPCGuarded(ctx, cfg, nil)
}

// A session client keeps one established socket, which cannot reconnect or
// retarget a replacement JVM. The guard brackets discovery and every request
// so a changed generation cannot authorize a subsequent operation or result.
func dialRPCGuarded(ctx context.Context, cfg instance.ManagementConfig, check func(context.Context) error) (*rpcClient, error) {
	if cfg.Host != "127.0.0.1" || cfg.Port < 1 || cfg.Port > 65535 || len(cfg.Secret) != 40 {
		return nil, domain.E(domain.CodeUnavailable, "private native management configuration is invalid")
	}
	for _, r := range cfg.Secret {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return nil, domain.E(domain.CodeUnavailable, "private native management secret is invalid")
		}
	}
	operation, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// No proxy, redirects, caller URL, browser exposure or TLS downgrade over a
	// remotely supplied host: the only endpoint is manager-provisioned loopback.
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if check != nil {
		if err := check(operation); err != nil {
			return nil, err
		}
	}
	conn, _, err := websocket.Dial(operation, "ws://127.0.0.1:"+strconv.Itoa(cfg.Port), &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + cfg.Secret}}})
	if err != nil {
		return nil, domain.E(domain.CodeUnavailable, "authenticated native management connection failed")
	}
	conn.SetReadLimit(1 << 20)
	c := &rpcClient{conn: conn, protocol: cfg.Protocol, methods: map[string]rpcMethod{}, check: check}
	raw, err := c.call(operation, "rpc.discover", nil)
	if err != nil {
		c.Close()
		return nil, err
	}
	var discovery struct {
		OpenRPC string `json:"openrpc"`
		Info    struct {
			Version string `json:"version"`
		} `json:"info"`
		Methods []rpcMethod `json:"methods"`
	}
	if json.Unmarshal(raw, &discovery) != nil || discovery.OpenRPC == "" || discovery.Info.Version != cfg.Protocol || len(discovery.Methods) == 0 || len(discovery.Methods) > 256 {
		c.Close()
		return nil, domain.E(domain.CodeUnsupported, "native management discovery or protocol version is unsupported")
	}
	for _, m := range discovery.Methods {
		if m.Name == "" || len(m.Name) > 256 {
			c.Close()
			return nil, domain.E(domain.CodeUnsupported, "native management discovery contains invalid methods")
		}
		if _, exists := c.methods[m.Name]; exists {
			c.Close()
			return nil, domain.E(domain.CodeUnsupported, "native management discovery contains duplicate methods")
		}
		c.methods[m.Name] = m
	}
	return c, nil
}
func (c *rpcClient) Close() { _ = c.conn.CloseNow() }
func (c *rpcClient) has(method, param, result string) bool {
	m, ok := c.methods[method]
	return ok && m.Result != nil && m.Result.Name == result && (param == "" && m.Params == nil || param != "" && m.Params != nil && m.Params.Name == param)
}
func (c *rpcClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	operation, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(nonce[:])
	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		request["params"] = params
	}
	if c.check != nil {
		if err := c.check(operation); err != nil {
			return nil, err
		}
	}
	if err := wsjson.Write(operation, c.conn, request); err != nil {
		return nil, domain.E(domain.CodeUnavailable, "native management request could not be delivered")
	}
	for skipped := 0; skipped < 64; skipped++ {
		var response struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Result  json.RawMessage `json:"result"`
			Error   json.RawMessage `json:"error"`
		}
		if err := wsjson.Read(operation, c.conn, &response); err != nil {
			return nil, domain.E(domain.CodeUnavailable, "native management response was unavailable or malformed")
		}
		if response.JSONRPC != "2.0" {
			return nil, domain.E(domain.CodeUnsupported, "native management returned an unsupported envelope")
		}
		if response.Method != "" {
			if len(response.ID) != 0 || len(response.Result) != 0 || len(response.Error) != 0 {
				return nil, domain.E(domain.CodeUnavailable, "native management returned a mixed request and response envelope")
			}
			continue
		}
		var received string
		if json.Unmarshal(response.ID, &received) != nil || received != id {
			return nil, domain.E(domain.CodeUnavailable, "native management response did not match the fresh request")
		}
		if len(response.Error) > 0 {
			return nil, domain.E(domain.CodeUnavailable, "native management rejected the requested operation")
		}
		if len(response.Result) == 0 || string(response.Result) == "null" {
			return nil, domain.E(domain.CodeUnavailable, "native management returned no verifiable result")
		}
		if c.check != nil {
			if err := c.check(operation); err != nil {
				return nil, err
			}
		}
		return response.Result, nil
	}
	return nil, domain.E(domain.CodeUnavailable, "native management notification limit exceeded")
}

type nativeRule struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
	Type  string          `json:"type"`
}

func (c *rpcClient) readRules(ctx context.Context) ([]nativeRule, error) {
	if !c.has("minecraft:gamerules", "", "gamerules") {
		return nil, domain.E(domain.CodeUnsupported, "native gamerule discovery is unavailable")
	}
	raw, err := c.call(ctx, "minecraft:gamerules", nil)
	if err != nil {
		return nil, err
	}
	var rules []nativeRule
	if json.Unmarshal(raw, &rules) != nil || rules == nil || len(rules) > 2048 {
		return nil, domain.E(domain.CodeUnavailable, "native gamerule response was invalid")
	}
	return rules, nil
}
func (c *rpcClient) ruleValue(rules []nativeRule, key string) (bool, error) {
	var value bool
	found := false
	for _, rule := range rules {
		if rule.Key != key {
			continue
		}
		if found || rule.Type != "boolean" {
			return false, domain.E(domain.CodeUnsupported, "required native boolean rule is ambiguous or unsupported")
		}
		found = true
		if c.protocol == "1.0.0" {
			var text string
			if json.Unmarshal(rule.Value, &text) != nil || (text != "true" && text != "false") {
				return false, domain.E(domain.CodeUnavailable, "native rule value was invalid")
			}
			value = text == "true"
		} else if json.Unmarshal(rule.Value, &value) != nil || string(rule.Value) == "null" {
			return false, domain.E(domain.CodeUnavailable, "native boolean rule value was invalid")
		}
	}
	if !found {
		return false, domain.E(domain.CodeUnsupported, "required native rule was not discovered")
	}
	return value, nil
}
func (c *rpcClient) verify(ctx context.Context, desired []instance.DesiredRuntimeSetting, apply bool) (State, error) {
	state := State{ProcessRunning: true, ControlConnected: true, Status: "starting", Source: "authenticated_msmp", RuleScope: "native_server_rules", GameplayEffect: "not_verified", ObservedAt: time.Now().UTC(), Reason: "The native server has not acknowledged world readiness."}
	if !c.has("minecraft:server/status", "", "status") {
		return state, domain.E(domain.CodeUnsupported, "native readiness status method is unavailable")
	}
	raw, err := c.call(ctx, "minecraft:server/status", nil)
	if err != nil {
		return state, err
	}
	var status struct {
		Started *bool `json:"started"`
	}
	if json.Unmarshal(raw, &status) != nil || status.Started == nil {
		return state, domain.E(domain.CodeUnavailable, "native readiness result is invalid")
	}
	if !*status.Started {
		return state, nil
	}
	state.GameReady = true
	state.Status = "settings_unverified"
	for _, required := range desired {
		if required.Kind != "boolean_gamerule" || !required.Supported || required.Scope != "native_server_rules" {
			return state, domain.E(domain.CodeUnsupported, "a required native setting is unavailable")
		}
		rules, e := c.readRules(ctx)
		if e != nil {
			return state, e
		}
		value, e := c.ruleValue(rules, required.Rule)
		if e != nil {
			return state, e
		}
		if value != required.Value {
			if !apply {
				return state, domain.E(domain.CodeUnavailable, "required native setting differs from the desired value")
			}
			if !c.has("minecraft:gamerules/update", "gamerule", "gamerule") {
				return state, domain.E(domain.CodeUnsupported, "native rule update is unavailable")
			}
			var payload any = required.Value
			if c.protocol == "1.0.0" {
				payload = strconv.FormatBool(required.Value)
			}
			updated, e := c.call(ctx, "minecraft:gamerules/update", map[string]any{"gamerule": map[string]any{"key": required.Rule, "value": payload}})
			if e != nil {
				return state, e
			}
			var acknowledged nativeRule
			if json.Unmarshal(updated, &acknowledged) != nil {
				return state, domain.E(domain.CodeUnavailable, "native rule update returned an invalid acknowledgement")
			}
			v, e := c.ruleValue([]nativeRule{acknowledged}, required.Rule)
			if e != nil || v != required.Value {
				return state, domain.E(domain.CodeUnavailable, "native rule update was not acknowledged exactly")
			}
			rules, e = c.readRules(ctx)
			if e != nil {
				return state, e
			}
			value, e = c.ruleValue(rules, required.Rule)
			if e != nil {
				return state, e
			}
			if value != required.Value {
				return state, domain.E(domain.CodeUnavailable, "native rule readback did not match the desired value")
			}
		}
	}
	state.RequiredSettingsVerified = true
	state.ReadyForUse = true
	state.Status = "ready"
	state.Reason = "Fresh native readiness and required native rule values are verified. Plugin and mod gameplay overrides are unverified."
	return state, nil
}
