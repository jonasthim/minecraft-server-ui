// Package recovery implements local, stopped-manager administrator recovery.
package recovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

// Reset replaces only an existing enabled local administrator's password.
// The manager lock covers every database access through database close. Like
// launcher.AcquireManager, private paths trust the operating-system account;
// this is not a defense against another process controlled by that account.
func Reset(ctx context.Context, dataDir, username string, input io.Reader) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("offline recovery cancelled: %w", err)
	}
	username, err := auth.NormalizeUsername(username)
	if err != nil {
		return errors.New("invalid administrator username")
	}
	if err = validatePaths(dataDir); err != nil {
		return err
	}
	release, err := launcher.AcquireManager(dataDir)
	if err != nil {
		return errors.New("cannot acquire manager lock; stop the manager and use its operating-system account")
	}
	defer release()
	// Repeat validation after lock acquisition before SQLite can open sidecars.
	if err = validatePaths(dataDir); err != nil {
		return err
	}
	database, err := db.OpenExisting(ctx, filepath.Join(dataDir, "manager.db"))
	if err != nil {
		return safeError(ctx, "cannot open existing current-schema manager database; use the matching manager version")
	}
	defer func() {
		if closeErr := database.Close(); closeErr != nil {
			if resultErr == nil {
				resultErr = errors.New("password reset committed; unable to close manager database")
			} else {
				resultErr = errors.Join(resultErr, errors.New("unable to close manager database after recovery error"))
			}
		}
	}()
	return reset(ctx, database, username, input)
}

func validatePaths(dataDir string) error {
	if !filepath.IsAbs(dataDir) {
		return errors.New("data directory must be absolute")
	}
	for p := filepath.Clean(dataDir); ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return errors.New("data directory must exist and contain no symlinks or non-directory components")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		fi, err := os.Lstat(filepath.Join(dataDir, "manager.db") + suffix)
		if suffix != "" && os.IsNotExist(err) {
			continue
		}
		if err != nil || !fi.Mode().IsRegular() {
			return errors.New("manager database must exist with regular, non-symlink SQLite files")
		}
	}
	return nil
}

func reset(ctx context.Context, database *sql.DB, username string, input io.Reader) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return safeError(ctx, "cannot begin offline recovery")
	}
	defer tx.Rollback()
	settings := domain.DefaultSettings()
	var settingsJSON string
	err = tx.QueryRowContext(ctx, `SELECT doc FROM settings WHERE id=1`).Scan(&settingsJSON)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return safeError(ctx, "cannot read authentication policy")
	}
	if err == nil {
		settings = domain.Settings{}
		if err = json.Unmarshal([]byte(settingsJSON), &settings); err != nil {
			return errors.New("cannot read authentication policy")
		}
	}
	if !settings.Auth.LocalLoginEnabled {
		return errors.New("local login is disabled; offline password recovery cannot change authentication policy")
	}
	var id int64
	var role string
	var disabled int
	var hash sql.NullString
	var identities int
	err = tx.QueryRowContext(ctx, `SELECT id,role,disabled,password_hash,(SELECT COUNT(*) FROM user_identities WHERE user_id=users.id) FROM users WHERE username=?`, username).Scan(&id, &role, &disabled, &hash, &identities)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("existing enabled local administrator required")
	}
	if err != nil {
		return safeError(ctx, "cannot read administrator account")
	}
	if role != "admin" || disabled != 0 || !hash.Valid || hash.String == "" || identities != 0 {
		return errors.New("existing enabled local administrator required; SSO accounts must recover through their identity provider")
	}
	password, err := readPassword(ctx, input)
	if err != nil {
		return err
	}
	encoded, err := auth.HashPassword(password)
	if err != nil {
		return errors.New("cannot hash recovery password")
	}
	if err = ctx.Err(); err != nil {
		return fmt.Errorf("offline recovery cancelled: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE users SET password_hash=?,updated_at=? WHERE id=?`, []any{encoded, now, id}},
		{`DELETE FROM sessions WHERE user_id=?`, []any{id}},
		{`DELETE FROM api_tokens WHERE user_id=?`, []any{id}},
		{`INSERT INTO audit_log(ts,user_id,username,action,target,details_json) VALUES(?,NULL,'local-offline','auth.admin_reset_offline',?,?)`, []any{now, "user:" + strconv.FormatInt(id, 10), `{"method":"offline","source":"local-native-command","credentials":"[redacted]"}`}},
	} {
		if _, err = tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return safeError(ctx, "offline recovery transaction failed; no recovery changes committed")
		}
	}
	if err = tx.Commit(); err != nil {
		return safeError(ctx, "offline recovery commit failed; verify account state before retrying")
	}
	return nil
}

func readPassword(ctx context.Context, input io.Reader) (string, error) {
	if input == nil {
		return "", errors.New("password stdin is required")
	}
	// 128 password bytes, CRLF, and one byte to detect overflow. Read through
	// EOF so a second line or any trailing data cannot silently be discarded.
	data, err := io.ReadAll(io.LimitReader(input, 131))
	if err != nil {
		return "", errors.New("cannot read password stdin")
	}
	if err = ctx.Err(); err != nil {
		return "", fmt.Errorf("offline recovery cancelled: %w", err)
	}
	if len(data) > 130 {
		return "", errors.New("password stdin exceeds the allowed size")
	}
	value := strings.TrimSuffix(string(data), "\n")
	if len(value) != len(data) {
		value = strings.TrimSuffix(value, "\r")
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", errors.New("password stdin must contain exactly one password and optional LF or CRLF termination")
	}
	if err = auth.ValidatePassword(value); err != nil {
		return "", errors.New("password must contain 10-128 bytes")
	}
	return value, nil
}

// SQLite and arbitrary Reader errors can contain stored/input secrets. Preserve
// cancellation identity, but expose only fixed, non-secret diagnostic messages.
func safeError(ctx context.Context, message string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("offline recovery cancelled: %w", err)
	}
	return errors.New(message)
}
