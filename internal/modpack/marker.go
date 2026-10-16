package modpack

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"os"
	"time"
)

const markerName = "modpack-transaction.json"

type marker struct {
	Version    int    `json:"version"`
	InstanceID string `json:"instance_id"`
	PreviewID  string `json:"preview_id"`
}

func (s *Service) stateRoot(ctx context.Context, id string) (*os.Root, error) {
	dirs, e := s.paths.For(id)
	if e != nil {
		return nil, e
	}
	return safepath.OpenRoot(ctx, dirs.State)
}
func (s *Service) readMarker(ctx context.Context, id string) (marker, bool, error) {
	var m marker
	root, e := s.stateRoot(ctx, id)
	if os.IsNotExist(e) {
		return m, false, nil
	}
	if e != nil {
		return m, false, e
	}
	defer root.Close()
	raw, e := readFile(ctx, root, markerName, 1024)
	if os.IsNotExist(e) {
		return m, false, nil
	}
	if e != nil {
		return m, true, e
	}
	if json.Unmarshal(raw, &m) != nil || m.Version != 1 || m.InstanceID != id || !validHex(m.PreviewID, 32) {
		return m, true, conflict("pack boot blocker malformed")
	}
	canonical, _ := json.Marshal(m)
	if !bytes.Equal(raw, canonical) {
		return m, true, conflict("pack boot blocker is not canonical")
	}
	return m, true, nil
}
func (s *Service) markTransaction(ctx context.Context, id, preview string) error {
	m, present, e := s.readMarker(ctx, id)
	if e != nil {
		return e
	}
	if present {
		if m.PreviewID != preview {
			return conflict("foreign pack blocker")
		}
		return nil
	}
	root, e := s.stateRoot(ctx, id)
	if e != nil {
		return e
	}
	defer root.Close()
	raw, _ := json.Marshal(marker{1, id, preview})
	return atomicFile(ctx, root, markerName, "", raw, false)
}
func (s *Service) clearTransaction(ctx context.Context, id, preview string) error {
	m, present, e := s.readMarker(ctx, id)
	if e != nil || !present {
		return e
	}
	if m.PreviewID != preview {
		return conflict("foreign pack blocker")
	}
	root, e := s.stateRoot(ctx, id)
	if e != nil {
		return e
	}
	defer root.Close()
	raw, _ := json.Marshal(m)
	return atomicFile(ctx, root, markerName, digest(raw), nil, true)
}
func (s *Service) CheckReady(ctx context.Context, id string) error {
	if _, e := s.paths.For(id); e != nil {
		return e
	}
	if _, present, e := s.readMarker(ctx, id); e != nil {
		return e
	} else if present {
		return conflict("pack transaction requires recovery")
	}
	var count int
	if e := s.db.QueryRowContext(ctx, "SELECT count(*) FROM modpack_journals WHERE instance_id=?", id).Scan(&count); e != nil {
		return e
	}
	if count > 0 {
		return conflict("pack journal requires recovery")
	}
	return nil
}

// Reconcile must run while dispatch is paused and before native startup.
func (s *Service) Reconcile(ctx context.Context) error {
	if s.recovery == nil {
		return conflict("pack stopped recovery admission unavailable")
	}
	rows, e := s.db.QueryContext(ctx, "SELECT id FROM instances ORDER BY id")
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			break
		}
		ids = append(ids, id)
	}
	e = errors.Join(e, rows.Err(), rows.Close())
	if e != nil {
		return e
	}
	for _, id := range ids {
		m, present, e := s.readMarker(ctx, id)
		if e != nil {
			return e
		}
		var preview string
		e = s.db.QueryRowContext(ctx, "SELECT preview_id FROM modpack_journals WHERE instance_id=?", id).Scan(&preview)
		if errors.Is(e, sql.ErrNoRows) {
			if present {
				return conflict("orphan pack boot blocker requires review")
			}
			continue
		}
		if e != nil {
			return e
		}
		if present && m.PreviewID != preview {
			return conflict("pack blocker differs from journal")
		}
		e = s.recovery.WithStoppedForRecovery(ctx, id, func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			root, e := s.openServer(ctx, id)
			if e != nil {
				return e
			}
			defer root.Close()
			return s.recoverJournal(ctx, root, id)
		})
		outcome := "reconciled"
		if e != nil {
			outcome = "blocked_preserved"
		}
		auditContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		raw, _ := json.Marshal(map[string]string{"outcome": outcome})
		_, auditErr := s.db.ExecContext(auditContext, "INSERT INTO audit_log(ts,user_id,username,action,instance_id,target,details_json) VALUES(?,NULL,'system','modpack.reconcile',?,'pack',?)", time.Now().UTC().Format(time.RFC3339Nano), id, string(raw))
		cancel()
		if err := errors.Join(e, auditErr); err != nil {
			return err
		}
	}
	return nil
}
