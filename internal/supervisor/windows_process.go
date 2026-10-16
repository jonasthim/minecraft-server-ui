//go:build windows

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

func (s *Supervisor) platformRequireStopped(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
func (s *Supervisor) platformStart(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.Command(s.cfg.Executable, "launch", "--data-dir", s.cfg.DataDir, "--instance", id, "--stop-timeout", s.cfg.StopTimeout.String()) // request context never owns launcher/JVM lifetime
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	file, ok := stdin.(*os.File)
	if !ok {
		stdin.Close()
		return fmt.Errorf("launcher parent pipe is not a native file")
	}
	if err = cmd.Start(); err != nil {
		stdin.Close()
		return fmt.Errorf("start native launcher: %w", err)
	}
	running := &child{stdin: file, done: make(chan struct{})}
	s.mu.Lock()
	s.children[id] = running
	s.mu.Unlock()
	go func() {
		running.err = cmd.Wait()
		stdin.Close()
		close(running.done)
		s.mu.Lock()
		if s.children[id] == running {
			delete(s.children, id)
		}
		s.mu.Unlock()
	}()
	return nil
}
func (s *Supervisor) platformSetAutostart(ctx context.Context, id string, enabled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := launcher.Paths(s.cfg.DataDir, id)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(p.State, 0700); err != nil {
		return err
	}
	return launcher.WriteJSON(filepath.Join(p.State, "autostart.json"), struct {
		Enabled bool `json:"enabled"`
	}{enabled})
}

// Autostart first reconciles every prior launcher and WAITS for its JVM and
// launcher lock to exit. An unreadable identity or control channel blocks startup
// instead of creating a second JVM holding the same ports/world files.
func (s *Supervisor) platformAutostart(ctx context.Context, specs map[string]domain.LaunchSpec) error {
	entries, err := os.ReadDir(filepath.Join(s.cfg.DataDir, "instances"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		if err = domain.ValidateInstanceID(id); err != nil {
			return err
		}
		identity, e := s.identity(id)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		alive, e := s.probe(ctx, identity)
		if e != nil {
			return e
		}
		if alive {
			if e = s.Stop(ctx, identity); e != nil {
				return fmt.Errorf("prior JVM did not stop before autostart: %w", e)
			}
		}
		release, e := launcher.AcquireAdmission(s.cfg.DataDir, id)
		if e != nil {
			return fmt.Errorf("prior launcher still active: %w", e)
		}
		release()
	}
	for _, id := range sortedIDs(specs) {
		p, e := launcher.Paths(s.cfg.DataDir, id)
		if e != nil {
			return e
		}
		var state struct {
			Enabled bool `json:"enabled"`
		}
		e = launcher.ReadJSON(filepath.Join(p.State, "autostart.json"), &state)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if state.Enabled {
			if _, e = s.Start(ctx, id, specs[id]); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *Supervisor) platformShutdown(ctx context.Context) error {
	s.mu.Lock()
	children := make([]*child, 0, len(s.children))
	for _, c := range s.children {
		children = append(children, c)
	}
	s.mu.Unlock()
	// Closing each parent pipe triggers graceful stop in every launcher. Wait for
	// all of them, including their bounded force/Wait path, before service exit.
	for _, c := range children {
		_ = c.stdin.Close()
	}
	var wg sync.WaitGroup
	results := make(chan error, len(children))
	for _, c := range children {
		wg.Add(1)
		go func(c *child) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				results <- ctx.Err()
			case <-c.done:
				if c.err != nil {
					results <- fmt.Errorf("launcher shutdown: %w", c.err)
				}
			}
		}(c)
	}
	wg.Wait()
	close(results)
	var joined error
	for err := range results {
		joined = errors.Join(joined, err)
	}
	return joined
}
