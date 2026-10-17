package instance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"strings"
	"time"
)

// BackupProperties preserves ordinary server configuration byte lines while
// removing manager-owned private management/RCON controls by decoded Java key.
// It uses the same continuation and escape grammar as configuration recovery.
func BackupProperties(raw []byte) ([]byte, error) {
	if len(raw) > 1<<20 {
		return nil, domain.E(domain.CodeValidationFailed, "server properties exceed backup limit")
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var out, original, logical strings.Builder
	continued := false
	flush := func() error {
		key, _, ok, err := decodeProperty(logical.String())
		if err != nil {
			return err
		}
		if !ok || !(strings.HasPrefix(key, "management-server-") || strings.HasPrefix(key, "rcon.") || key == "enable-rcon") {
			out.WriteString(original.String())
		}
		original.Reset()
		logical.Reset()
		return nil
	}
	for scanner.Scan() {
		part := scanner.Text()
		// Comments terminate at their natural newline even when they end in a
		// backslash. A continuation line is already property data, so its first
		// non-whitespace # or ! must not start a comment.
		if !continued {
			trimmed := strings.TrimLeft(part, " \t\f")
			if len(trimmed) > 0 && (trimmed[0] == '#' || trimmed[0] == '!') {
				out.WriteString(part)
				out.WriteByte('\n')
				continue
			}
		}
		original.WriteString(part)
		original.WriteByte('\n')
		if continued {
			part = strings.TrimLeft(part, " \t\f")
		}
		n := 0
		for j := len(part) - 1; j >= 0 && part[j] == '\\'; j-- {
			n++
		}
		continued = n%2 == 1
		if continued {
			logical.WriteString(part[:len(part)-1])
			continue
		}
		logical.WriteString(part)
		if err := flush(); err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if original.Len() > 0 {
		if err := flush(); err != nil {
			return nil, err
		}
	}
	return []byte(out.String()), nil
}

// MaterializeRestoredProperties replaces only ordinary unknown persisted
// properties from the restored file. Typed settings and private management
// configuration keep their current authoritative DB values. A failed file
// materialization leaves the already-committed Extra values for forward retry.
func (s *StoppedSession) MaterializeRestoredProperties(ctx context.Context) error {
	scope, release, err := s.enter(ctx)
	if err != nil {
		return err
	}
	defer release()
	operation, cancel := sessionContext(ctx, scope.ctx)
	defer cancel()
	return scope.service.materializeRestoredProperties(operation, scope.id)
}

// materializeRestoredProperties is shared by ordinary restore and a retained
// coordinator scope, after its pointer transaction has closed.
func (s *Service) materializeRestoredProperties(ctx context.Context, id string) error {
	detail, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	dirs, err := s.paths.For(id)
	if err != nil {
		return err
	}
	root, err := safepath.OpenRoot(ctx, dirs.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = s.verifyOwner(ctx, id, root); err != nil {
		return err
	}
	server, err := safepath.OpenRoot(ctx, dirs.Server)
	if err != nil {
		return err
	}
	defer server.Close()
	extra, err := unknownPropertiesAt(server, "server.properties")
	if err != nil {
		return err
	}
	detail.Config.Extra = extra
	if err = ValidateConfig(detail.Config); err != nil {
		return err
	}
	raw, err := json.Marshal(detail.Config)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE instances SET config_json=?,updated_at=? WHERE id=? AND updated_at=?`, string(raw), time.Now().UTC().Format(time.RFC3339Nano), id, detail.Instance.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.E(domain.CodeConflict, "instance settings changed during backup restore")
	}
	return s.materialize(ctx, id)
}
