package content

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"os"
)

const markerName = "content-transaction.json"

type transactionMarker struct {
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
func (s *Service) readMarker(ctx context.Context, id string) (transactionMarker, bool, error) {
	var marker transactionMarker
	root, e := s.stateRoot(ctx, id)
	if os.IsNotExist(e) {
		return marker, false, nil
	}
	if e != nil {
		return marker, false, e
	}
	defer root.Close()
	raw, e := readFile(ctx, root, markerName, 1024)
	if os.IsNotExist(e) {
		return marker, false, nil
	}
	if e != nil {
		return marker, true, e
	}
	if json.Unmarshal(raw, &marker) != nil || marker.Version != 1 || marker.InstanceID != id || len(marker.PreviewID) != 32 || !portable(marker.PreviewID) {
		return marker, true, conflict("content transaction blocker is malformed or belongs to another instance")
	}
	canonical, _ := json.Marshal(marker)
	if !bytes.Equal(raw, canonical) {
		return marker, true, conflict("content transaction blocker encoding is not canonical")
	}
	return marker, true, nil
}
func (s *Service) markTransaction(ctx context.Context, id, preview string) error {
	marker, present, e := s.readMarker(ctx, id)
	if e != nil {
		return e
	}
	if present {
		if marker.PreviewID != preview {
			return conflict("another content transaction owns the boot blocker")
		}
		return nil
	}
	root, e := s.stateRoot(ctx, id)
	if e != nil {
		return e
	}
	defer root.Close()
	raw, _ := json.Marshal(transactionMarker{1, id, preview})
	return atomicFile(ctx, root, markerName, "", raw, false)
}
func (s *Service) clearTransaction(ctx context.Context, id, preview string) error {
	marker, present, e := s.readMarker(ctx, id)
	if e != nil {
		return e
	}
	if !present {
		return nil
	}
	if marker.PreviewID != preview {
		return conflict("content recovery cannot clear a foreign boot blocker")
	}
	root, e := s.stateRoot(ctx, id)
	if e != nil {
		return e
	}
	defer root.Close()
	raw, _ := json.Marshal(marker)
	return atomicFile(ctx, root, markerName, digest(raw), nil, true)
}

// CheckReady gates ordinary lifecycle/config/content mutations. Recovery uses
// paused stopped admission and deliberately bypasses this ordinary gate.
func (s *Service) CheckReady(ctx context.Context, id string) error {
	if _, e := s.paths.For(id); e != nil {
		return e
	}
	var count int
	if e := s.db.QueryRowContext(ctx, "SELECT count(*) FROM content_journals WHERE instance_id=?", id).Scan(&count); e != nil {
		return e
	}
	_, present, e := s.readMarker(ctx, id)
	if e != nil {
		return e
	}
	if count > 0 || present {
		return conflict("content transaction requires stopped recovery before another mutation")
	}
	return nil
}
func (s *Service) verifyMarkerJournal(ctx context.Context, id string) error {
	marker, present, e := s.readMarker(ctx, id)
	if e != nil {
		return e
	}
	if !present {
		return nil
	}
	var preview string
	e = s.db.QueryRowContext(ctx, "SELECT preview_id FROM content_journals WHERE instance_id=?", id).Scan(&preview)
	if errors.Is(e, sql.ErrNoRows) {
		return conflict("content boot blocker has no durable SQL journal; preserve it for review")
	}
	if e != nil {
		return e
	}
	if preview != marker.PreviewID {
		return conflict("content boot blocker and durable SQL journal disagree")
	}
	return nil
}
