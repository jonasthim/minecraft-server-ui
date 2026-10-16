package console

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

const maxLineBytes = 8192
const retainedLines = 2000
const discardBoundaryPrefix = "discard-through-newline:"

// Line contains redacted application text, never raw console file content.
type Line struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Message   string    `json:"message"`
	Gap       bool      `json:"gap"`
}
type Page struct {
	Lines      []Line `json:"lines"`
	NextCursor int64  `json:"next_cursor"`
	Gap        bool   `json:"gap"`
}
type History struct {
	db       *sql.DB
	paths    instance.Paths
	mu       sync.Mutex
	secrets  map[string][]string
	maxLines int
}

func NewHistory(database *sql.DB, paths instance.Paths) *History {
	return &History{db: database, paths: paths, secrets: map[string][]string{}, maxLines: retainedLines}
}
func (h *History) SetSecrets(id string, secrets []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.secrets[id] = append([]string(nil), secrets...)
}

type rawFile struct {
	f    *os.File
	size int64
}

func boundary(f *os.File, offset int64) (string, error) {
	size := min(offset, 256)
	buf := make([]byte, size)
	if _, err := f.ReadAt(buf, offset-size); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}

// Capture resumes at a durable newline boundary or an explicit durable discard
// checkpoint. Partial Unicode and secrets stay private until a complete line is
// redacted, or are discarded through their terminating newline across captures.
// Lost/truncated/ambiguous raw history produces an explicit retained gap line.
func (h *History) Capture(ctx context.Context, id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	dirs, err := h.paths.For(id)
	if err != nil {
		return err
	}
	root, err := safepath.OpenRoot(ctx, dirs.Logs)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open private console logs: %w", err)
	}
	defer root.Close()
	files := []rawFile{}
	defer func() {
		for _, file := range files {
			file.f.Close()
		}
	}()
	for _, name := range []string{"console.log.2", "console.log.1", "console.log"} {
		info, e := root.Lstat(name)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return domain.E(domain.CodeValidationFailed, "console log must be a regular file")
		}
		f, e := root.Open(name)
		if e != nil {
			return e
		}
		stat, e := f.Stat()
		if e != nil {
			f.Close()
			return e
		}
		if !os.SameFile(info, stat) || !stat.Mode().IsRegular() {
			f.Close()
			return domain.E(domain.CodeConflict, "console log changed while opening")
		}
		if stat.Size() > 4<<20 {
			f.Close()
			return domain.E(domain.CodeValidationFailed, "raw console file exceeds its rotation bound")
		}
		files = append(files, rawFile{f, stat.Size()})
	}
	if len(files) == 0 {
		return nil
	}
	var offset int64
	var hash string
	err = h.db.QueryRowContext(ctx, `SELECT byte_offset,boundary_hash FROM console_cursors WHERE instance_id=?`, id).Scan(&offset, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read console cursor: %w", err)
	}
	start := 0
	gap := false
	discard := strings.HasPrefix(hash, discardBoundaryPrefix)
	hash = strings.TrimPrefix(hash, discardBoundaryPrefix)
	if hash != "" {
		matches := []int{}
		for i, file := range files {
			if file.size >= offset {
				v, e := boundary(file.f, offset)
				if e == nil && v == hash {
					matches = append(matches, i)
				}
			}
		}
		if len(matches) == 1 {
			start = matches[0]
		} else {
			offset = 0
			gap = true
			// The retained oldest bytes may continue a secret whose prefix was
			// lost with the checkpoint. Skip through the first complete newline.
			discard = true
		}
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	appendLine := func(message string, gap bool) error {
		_, e := tx.ExecContext(ctx, `INSERT INTO console_lines(instance_id,created_at,message,gap) VALUES(?,?,?,?)`, id, time.Now().UTC().Format(time.RFC3339Nano), message, gap)
		return e
	}
	if gap {
		if err = appendLine("[Console history gap: raw logs rotated, were truncated, or could not be matched after reconnect.]", true); err != nil {
			return err
		}
	}
	// At most the launcher's retained 12 MiB is read in a capture. Keep only one
	// bounded logical line; discard oversized lines entirely to avoid a cut secret.
	var pending []byte
	oversized := false
	var committed int64
	committedHash := hash
	for i := start; i < len(files); i++ {
		file := files[i]
		begin := int64(0)
		if i == start {
			begin = offset
		}
		committed = begin
		reader := io.NewSectionReader(file.f, begin, file.size-begin)
		buf := make([]byte, 32<<10)
		position := begin
		for {
			if err = ctx.Err(); err != nil {
				return err
			}
			n, e := reader.Read(buf)
			if n > 0 {
				for _, b := range buf[:n] {
					position++
					if discard {
						if b == '\n' {
							discard = false
							committed = position
						}
						continue
					}
					if b == '\n' {
						message := "[Console line omitted: exceeded 8192 bytes.]"
						if !oversized {
							message = Redact(string(bytes.TrimSuffix(pending, []byte{'\r'})), h.secrets[id])
						}
						if err = appendLine(message, oversized); err != nil {
							return err
						}
						pending = nil
						oversized = false
						committed = position
					} else if !oversized {
						if len(pending) >= maxLineBytes {
							pending = nil
							oversized = true
						} else {
							pending = append(pending, b)
						}
					}
				}
			}
			if e == io.EOF {
				break
			}
			if e != nil {
				return fmt.Errorf("read private console: %w", e)
			}
		}
		if i < len(files)-1 { // Rotation may split a UTF-8 code point or logical line.
			continue
		}
		// A partial logical line spanning files cannot be resumed using this
		// single-file checkpoint. Persist discarding through its next newline;
		// advancing the cursor alone would expose the continuation as a new line.
		if len(pending) > 0 || oversized {
			if begin == 0 && i > start {
				if err = appendLine("[Console history gap: partial line crossed a rotation boundary.]", true); err != nil {
					return err
				}
				committed = file.size
				discard = true
			}
		}
		if discard {
			committed = file.size
		}
		committedHash, err = boundary(file.f, committed)
		if err != nil {
			return err
		}
		if discard {
			committedHash = discardBoundaryPrefix + committedHash
		}
	}
	// If a split line crossed a file boundary and completed, all its bytes were
	// consumed and the checkpoint is in the newest file. Empty newest files are
	// also valid checkpoints (hash of empty bytes).
	_, err = tx.ExecContext(ctx, `INSERT INTO console_cursors(instance_id,byte_offset,boundary_hash) VALUES(?,?,?) ON CONFLICT(instance_id) DO UPDATE SET byte_offset=excluded.byte_offset,boundary_hash=excluded.boundary_hash`, id, committed, committedHash)
	if err != nil {
		return err
	}
	var cutoff sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT id FROM console_lines WHERE instance_id=? ORDER BY id DESC LIMIT 1 OFFSET ?`, id, h.maxLines).Scan(&cutoff)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if cutoff.Valid {
		if _, err = tx.ExecContext(ctx, `DELETE FROM console_lines WHERE instance_id=? AND id<=?`, id, cutoff.Int64); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE console_cursors SET pruned_before=MAX(pruned_before,?) WHERE instance_id=?`, cutoff.Int64, id); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit redacted console: %w", err)
	}
	return nil
}
func (h *History) Read(ctx context.Context, id string, after int64, limit int) (Page, error) {
	out := Page{Lines: []Line{}, NextCursor: after}
	if _, err := h.paths.For(id); err != nil {
		return out, err
	}
	if after < 0 || limit < 1 || limit > 500 {
		return out, domain.E(domain.CodeValidationFailed, "invalid console cursor or limit")
	}
	var exists int
	if err := h.db.QueryRowContext(ctx, `SELECT 1 FROM instances WHERE id=?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return out, domain.NotFound("instance")
	} else if err != nil {
		return out, err
	}
	var latest sql.NullInt64
	if err := h.db.QueryRowContext(ctx, `SELECT MAX(id) FROM console_lines WHERE instance_id=?`, id).Scan(&latest); err != nil {
		return out, err
	}
	// A cursor from another instance or a removed database must not suppress all
	// future output forever. Replay retained history with an explicit gap.
	if after > latest.Int64 {
		out.Gap = true
		after = 0
		out.NextCursor = 0
	}
	var pruned int64
	err := h.db.QueryRowContext(ctx, `SELECT pruned_before FROM console_cursors WHERE instance_id=?`, id).Scan(&pruned)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	out.Gap = out.Gap || after > 0 && after < pruned
	rows, err := h.db.QueryContext(ctx, `SELECT id,created_at,message,gap FROM console_lines WHERE instance_id=? AND id>? ORDER BY id LIMIT ?`, id, after, limit)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var line Line
		var stamp string
		if err = rows.Scan(&line.ID, &stamp, &line.Message, &line.Gap); err != nil {
			return out, err
		}
		line.CreatedAt, err = time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return out, err
		}
		out.Lines = append(out.Lines, line)
		out.NextCursor = line.ID
	}
	return out, rows.Err()
}
