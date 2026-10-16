package console

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

type sessionScope struct {
	owner    *Session
	active   atomic.Bool
	mu       sync.Mutex
	ctx      context.Context
	service  *Service
	identity domain.ProcessIdentity
	detail   instance.Detail
}

// Session is a typed capability valid only as the original callback argument.
// It does not admit arbitrary commands or reacquire the queue admission.
type Session struct{ scope *sessionScope }

// Identity returns the original live session's JVM generation by value.
func (s *Session) Identity(ctx context.Context) (domain.ProcessIdentity, error) {
	scope, release, err := s.enter(ctx, false)
	if err != nil {
		return domain.ProcessIdentity{}, err
	}
	defer release()
	operation, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(scope.ctx, cancel)
	defer stop()
	if err := scope.check(operation); err != nil {
		return domain.ProcessIdentity{}, err
	}
	return scope.identity, nil
}

type PlayerObservation struct {
	Count      int
	Known      bool
	Source     string
	ObservedAt time.Time
}

// ObservePlayers records only a fresh acknowledged count, never a player identity.
func (s *Session) ObservePlayers(ctx context.Context) (PlayerObservation, error) {
	count, err := s.Players(ctx)
	observation := PlayerObservation{ObservedAt: time.Now().UTC()}
	if err != nil {
		return observation, err
	}
	observation.Count = count
	observation.Known = true
	observation.Source = "fresh_native_console_list"
	if _, err := instance.ManagementProtocol(s.scope.detail.Instance.MinecraftVersion); err == nil {
		observation.Source = "authenticated_msmp_players"
	}
	return observation, nil
}

func (s *Service) WithJobSession(ctx context.Context, e *jobs.Execution, fn func(*Session) error) error {
	if e == nil || fn == nil {
		return domain.E(domain.CodeValidationFailed, "console job session requires a live execution and callback")
	}
	return e.WithInstanceMutation(ctx, func(id string) error { return s.withSession(ctx, id, fn) })
}
func (s *Service) withSession(ctx context.Context, id string, fn func(*Session) error) error {
	release, err := s.lock(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	if s.instances == nil || s.native == nil {
		return domain.E(domain.CodeUnavailable, "native console is unavailable")
	}
	detail, err := s.instances.Get(ctx, id)
	if err != nil {
		return err
	}
	identity, err := s.native.Identity(ctx, id)
	if err != nil {
		return domain.E(domain.CodeUnavailable, "managed JVM identity is unavailable")
	}
	observed, err := s.native.Inspect(ctx, identity)
	if err != nil || !observed.ProcessRunning || observed.State != domain.StateRunning || observed.WatchdogError != "" {
		return domain.E(domain.CodeUnavailable, "managed JVM is not running with a healthy control channel")
	}
	session := &Session{}
	scope := &sessionScope{owner: session, ctx: ctx, service: s, identity: identity, detail: detail}
	session.scope = scope
	scope.active.Store(true)
	defer func() { scope.active.Store(false); scope.mu.Lock(); scope.mu.Unlock() }()
	return fn(session)
}
func (s *Session) enter(ctx context.Context, cleanup bool) (*sessionScope, func(), error) {
	if s == nil || s.scope == nil || s.scope.owner != s || !s.scope.active.Load() {
		return nil, nil, domain.E(domain.CodeForbidden, "console session is invalid or expired")
	}
	scope := s.scope
	if !scope.mu.TryLock() {
		return nil, nil, domain.E(domain.CodeInstanceBusy, "another console session method is active")
	}
	if !scope.active.Load() {
		scope.mu.Unlock()
		return nil, nil, domain.E(domain.CodeForbidden, "console session expired")
	}
	if err := ctx.Err(); err != nil {
		scope.mu.Unlock()
		return nil, nil, err
	}
	if !cleanup {
		if err := scope.ctx.Err(); err != nil {
			scope.mu.Unlock()
			return nil, nil, err
		}
	}
	return scope, scope.mu.Unlock, nil
}
func (scope *sessionScope) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	identity, err := scope.service.native.Identity(ctx, scope.identity.InstanceID)
	if err != nil || identity != scope.identity {
		return domain.E(domain.CodeUnavailable, "managed JVM generation changed during command verification")
	}
	observed, err := scope.service.native.Inspect(ctx, identity)
	if err != nil || !observed.ProcessRunning || observed.State != domain.StateRunning || observed.WatchdogError != "" {
		return domain.E(domain.CodeUnavailable, "managed JVM is no longer ready for command verification")
	}
	return ctx.Err()
}
func (scope *sessionScope) rpc(ctx context.Context) (*rpcClient, error) {
	if err := scope.check(ctx); err != nil {
		return nil, err
	}
	cfg, err := scope.service.instances.Management(ctx, scope.identity.InstanceID)
	if err != nil {
		return nil, err
	}
	scope.service.history.SetSecrets(scope.identity.InstanceID, []string{cfg.Secret})
	return dialRPCGuarded(ctx, cfg, scope.check)
}
func (scope *sessionScope) legacy(ctx context.Context, command string, ack acknowledgement) (domain.CommandResult, error) {
	result := domain.CommandResult{}
	if err := scope.check(ctx); err != nil {
		return result, err
	}
	cursor, err := newRawCursor(scope.service.paths, scope.identity.InstanceID)
	if err != nil {
		return result, domain.E(domain.CodeUnavailable, "a fresh native console cursor could not be established")
	}
	if err = scope.service.native.Command(ctx, scope.identity, command); err != nil {
		return result, domain.E(domain.CodeUnavailable, "native console command could not be delivered")
	}
	result.Received = true
	output, err := cursor.wait(ctx, ack)
	if err != nil {
		return result, err
	}
	if err = scope.check(ctx); err != nil {
		return result, err
	}
	result.Output = Redact(output, nil)
	result.Verified = true
	return result, nil
}
func (s *Session) VerifyReady(ctx context.Context) (State, error) { return s.verify(ctx, true) }
func (s *Session) verify(ctx context.Context, apply bool) (State, error) {
	scope, release, err := s.enter(ctx, false)
	if err != nil {
		return State{}, err
	}
	defer release()
	operation, cancel := commandTimeout(ctx)
	defer cancel()
	stop := context.AfterFunc(scope.ctx, cancel)
	defer stop()
	state := State{ProcessRunning: true, Status: "unknown", Reason: "Fresh native readiness could not be verified.", GameplayEffect: "not_verified", RuleScope: "native_server_rules", ObservedAt: time.Now().UTC()}
	if _, modern := instance.ManagementProtocol(scope.detail.Instance.MinecraftVersion); modern == nil {
		client, e := scope.rpc(operation)
		if e != nil {
			scope.service.store(scope.identity, state)
			return state, e
		}
		defer client.Close()
		state, e = client.verify(operation, scope.detail.Settings.DesiredRuntime, apply)
		if e != nil {
			state.Reason = domain.AsError(e).Message
			scope.service.store(scope.identity, state)
			return state, e
		}
	} else {
		if _, e := ValidateCommand(selectionOf(scope.detail), domain.RoleOperator, "list"); e != nil {
			return state, e
		}
		result, e := scope.legacy(operation, "list", ackList(scope.detail.Instance.MinecraftVersion))
		if e != nil {
			scope.service.store(scope.identity, state)
			return state, e
		}
		state.ControlConnected = result.Received
		state.GameReady = result.Verified
		state.RequiredSettingsVerified = len(scope.detail.Settings.DesiredRuntime) == 0
		state.ReadyForUse = state.GameReady && state.RequiredSettingsVerified
		state.Source = "fresh_native_console_list"
		state.Status = "ready"
		state.Reason = "Fresh native list acknowledgement verified server command processing. Gameplay overrides are unverified."
	}
	if err = scope.check(operation); err != nil {
		state.ReadyForUse = false
		state.GameReady = false
		state.Status = "unknown"
		state.Reason = domain.AsError(err).Message
		scope.service.store(scope.identity, state)
		return state, err
	}
	scope.service.store(scope.identity, state)
	return state, nil
}
func (s *Session) public(ctx context.Context, command PublicCommand) (domain.CommandResult, error) {
	scope, release, err := s.enter(ctx, false)
	if err != nil {
		return domain.CommandResult{}, err
	}
	defer release()
	operation, cancel := commandTimeout(ctx)
	defer cancel()
	stop := context.AfterFunc(scope.ctx, cancel)
	defer stop()
	// list maps to the discovered native method on MSMP versions; seed and say
	// retain the exact console grammar and a fresh server-thread response oracle.
	if command.Verb == "list" {
		if _, e := instance.ManagementProtocol(scope.detail.Instance.MinecraftVersion); e == nil {
			client, e := scope.rpc(operation)
			if e != nil {
				return domain.CommandResult{}, e
			}
			defer client.Close()
			if !client.has("minecraft:players", "", "players") {
				return domain.CommandResult{}, domain.E(domain.CodeUnsupported, "native player listing is unavailable")
			}
			raw, e := client.call(operation, "minecraft:players", nil)
			if e != nil {
				return domain.CommandResult{Received: true}, e
			}
			var players []struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &players) != nil || players == nil || len(players) > 10000 {
				return domain.CommandResult{Received: true}, domain.E(domain.CodeUnavailable, "native player listing returned invalid data")
			}
			if e = scope.check(operation); e != nil {
				return domain.CommandResult{Received: true}, e
			}
			return domain.CommandResult{Received: true, Verified: true, Output: fmt.Sprintf("%d players online", len(players))}, nil
		}
	}
	return scope.legacy(operation, command.Text, ackCommand(selectionOf(scope.detail), command))
}

// Announce is available only to an already-authorized per-instance job session.
func (s *Session) Announce(ctx context.Context, message string) (domain.CommandResult, error) {
	if s == nil || s.scope == nil {
		return domain.CommandResult{}, domain.E(domain.CodeForbidden, "invalid console session")
	}
	command, err := ValidateCommand(selectionOf(s.scope.detail), domain.RoleOperator, "say "+message)
	if err != nil {
		return domain.CommandResult{}, err
	}
	return s.public(ctx, command)
}

// Flush requires a fresh synchronous native result or exact post-write console
// completion. A stdin write alone always leaves Verified false.
func (s *Session) Flush(ctx context.Context) (domain.CommandResult, error) {
	scope, release, err := s.enter(ctx, false)
	if err != nil {
		return domain.CommandResult{}, err
	}
	defer release()
	operation, cancel := commandTimeout(ctx)
	defer cancel()
	stop := context.AfterFunc(scope.ctx, cancel)
	defer stop()
	if _, e := instance.ManagementProtocol(scope.detail.Instance.MinecraftVersion); e == nil {
		client, e := scope.rpc(operation)
		if e != nil {
			return domain.CommandResult{}, e
		}
		defer client.Close()
		if !client.has("minecraft:server/save", "flush", "saving") {
			return domain.CommandResult{}, domain.E(domain.CodeUnsupported, "native flush save method is unavailable")
		}
		raw, e := client.call(operation, "minecraft:server/save", map[string]bool{"flush": true})
		if e != nil {
			return domain.CommandResult{Received: true}, e
		}
		var saved bool
		if json.Unmarshal(raw, &saved) != nil || !saved {
			return domain.CommandResult{Received: true}, domain.E(domain.CodeUnavailable, "native flush save was not acknowledged")
		}
		if e = scope.check(operation); e != nil {
			return domain.CommandResult{Received: true}, e
		}
		return domain.CommandResult{Received: true, Verified: true, Output: "Native flush save completed."}, nil
	}
	return scope.legacy(operation, "save-all flush", ackFlush(scope.detail.Instance.MinecraftVersion))
}

// ReleaseSaving may use a caller's independent cleanup context even after the
// original job context is cancelled. The live callback still owns admission.
// It always attempts native release, including when establishing a log cursor
// fails, and reports Verified only after a fresh native completion response.
func (s *Session) ReleaseSaving(ctx context.Context, leaseID string) (domain.CommandResult, error) {
	scope, release, err := s.enter(ctx, true)
	if err != nil {
		return domain.CommandResult{}, err
	}
	defer release()
	operation, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := domain.CommandResult{}
	cursor, cursorErr := newRawCursor(scope.service.paths, scope.identity.InstanceID)
	if err = scope.service.native.ReleaseSaveLease(operation, scope.identity, leaseID); err != nil {
		return result, domain.E(domain.CodeUnavailable, "native save-on recovery could not be delivered")
	}
	result.Received = true

	if cursorErr != nil {
		return result, domain.E(domain.CodeUnavailable, "save-on was attempted but its fresh acknowledgement cursor is unavailable")
	}
	out, err := cursor.wait(operation, ackSaveOn(scope.detail.Instance.MinecraftVersion))
	if err != nil {
		return result, err
	}
	if err = scope.check(operation); err != nil {
		return result, err
	}
	result.Verified = true
	result.Output = Redact(out, nil)
	return result, nil
}

// Players returns an acknowledged count. Missing/stale/malformed observations
// are errors, never an inferred zero used to skip a restart warning.
func (s *Session) Players(ctx context.Context) (int, error) {
	scope, release, err := s.enter(ctx, false)
	if err != nil {
		return 0, err
	}
	defer release()
	operation, cancel := commandTimeout(ctx)
	defer cancel()
	stop := context.AfterFunc(scope.ctx, cancel)
	defer stop()
	if _, modern := instance.ManagementProtocol(scope.detail.Instance.MinecraftVersion); modern == nil {
		client, e := scope.rpc(operation)
		if e != nil {
			return 0, e
		}
		defer client.Close()
		if !client.has("minecraft:players", "", "players") {
			return 0, domain.E(domain.CodeUnsupported, "native player listing is unavailable")
		}
		raw, e := client.call(operation, "minecraft:players", nil)
		if e != nil {
			return 0, e
		}
		var players []struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &players) != nil || players == nil || len(players) > 10000 {
			return 0, domain.E(domain.CodeUnavailable, "native player listing returned invalid data")
		}
		if e = scope.check(operation); e != nil {
			return 0, e
		}
		return len(players), nil
	}
	result, err := scope.legacy(operation, "list", ackList(scope.detail.Instance.MinecraftVersion))
	if err != nil {
		return 0, err
	}
	remainder := strings.TrimPrefix(result.Output, "There are ")
	end := strings.IndexAny(remainder, "/ ")
	if end < 1 {
		return 0, domain.E(domain.CodeUnavailable, "native player count was invalid")
	}
	count, err := strconv.Atoi(remainder[:end])
	if err != nil || count < 0 || count > 10000 {
		return 0, domain.E(domain.CodeUnavailable, "native player count exceeded limits")
	}
	return count, nil
}
