package recovery

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

const oldPassword = "old-fixture-password"
const newPassword = "new-fixture-password"

type fixture struct {
	dir          string
	database     *sql.DB
	admin, other int64
	oldHash      string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state #?&")
	database, err := db.Open(t.Context(), filepath.Join(dir, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	service := auth.NewService(database, auth.Config{}, nil)
	admin, err := service.Setup(t.Context(), httptest.NewRecorder(), httptest.NewRequest("POST", "http://localhost/setup", nil), "admin", oldPassword, "Fixture admin", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.Create(t.Context(), "other", oldPassword, "Other", "", domain.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{admin.ID, other.ID} {
		for i := 0; i < 2; i++ {
			_, _, err = service.CreateToken(t.Context(), id, "fixture", 0)
			if err != nil {
				t.Fatal(err)
			}
		}
		_, err = service.Login(t.Context(), httptest.NewRecorder(), httptest.NewRequest("POST", "http://localhost/login", nil), map[bool]string{true: "admin", false: "other"}[id == admin.ID], oldPassword)
		if err != nil {
			t.Fatal(err)
		}
	}
	var hash string
	if err = database.QueryRow(`SELECT password_hash FROM users WHERE id=?`, admin.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	return fixture{dir, database, admin.ID, other.ID, hash}
}
func scalar(t *testing.T, database *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := database.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func hashFor(t *testing.T, f fixture) string {
	t.Helper()
	var hash string
	if err := f.database.QueryRow(`SELECT password_hash FROM users WHERE id=?`, f.admin).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	return hash
}
func unchanged(t *testing.T, f fixture) {
	t.Helper()
	if hashFor(t, f) != f.oldHash || scalar(t, f.database, `SELECT COUNT(*) FROM sessions WHERE user_id=?`, f.admin) != 2 || scalar(t, f.database, `SELECT COUNT(*) FROM api_tokens WHERE user_id=?`, f.admin) != 2 || scalar(t, f.database, `SELECT COUNT(*) FROM audit_log`) != 0 {
		t.Fatal("credential, sessions, tokens or audit changed on refusal")
	}
}

// Omitting revocation, changing another user, or accepting the old credential breaks this test.
func TestResetLocalAdminAtomicSuccess(t *testing.T) {
	f := newFixture(t)
	beforeOther, err := db.NewUsers(f.database).Get(t.Context(), f.other)
	if err != nil {
		t.Fatal(err)
	}
	if err = Reset(t.Context(), f.dir, " AdMiN ", strings.NewReader(newPassword+"\r\n")); err != nil {
		t.Fatal(err)
	}
	hash := hashFor(t, f)
	if !auth.VerifyPassword(hash, newPassword) || auth.VerifyPassword(hash, oldPassword) {
		t.Fatal("password was not replaced")
	}
	if scalar(t, f.database, `SELECT COUNT(*) FROM sessions WHERE user_id=?`, f.admin) != 0 || scalar(t, f.database, `SELECT COUNT(*) FROM api_tokens WHERE user_id=?`, f.admin) != 0 {
		t.Fatal("recovered account still has credentials")
	}
	afterOther, err := db.NewUsers(f.database).Get(t.Context(), f.other)
	if err != nil {
		t.Fatal(err)
	}
	if beforeOther.PasswordHash != afterOther.PasswordHash || scalar(t, f.database, `SELECT COUNT(*) FROM sessions WHERE user_id=?`, f.other) != 1 || scalar(t, f.database, `SELECT COUNT(*) FROM api_tokens WHERE user_id=?`, f.other) != 2 {
		t.Fatal("other account changed")
	}
	usr, err := db.NewUsers(f.database).Get(t.Context(), f.admin)
	if err != nil {
		t.Fatal(err)
	}
	if usr.Role != domain.RoleAdmin || usr.Disabled || usr.DisplayName != "Fixture admin" {
		t.Fatal("identity changed")
	}
	var action, details, target, username string
	var actor sql.NullInt64
	if err = f.database.QueryRow(`SELECT action,details_json,target,username,user_id FROM audit_log`).Scan(&action, &details, &target, &username, &actor); err != nil {
		t.Fatal(err)
	}
	if action != "auth.admin_reset_offline" || target != "user:1" || username != "local-offline" || actor.Valid || !strings.Contains(details, "offline") {
		t.Fatalf("missing offline audit attribution: %s %s %s %s", action, details, target, username)
	}
	for _, secret := range []string{oldPassword, newPassword, f.oldHash, hash} {
		if strings.Contains(details, secret) {
			t.Fatal("audit contains secret")
		}
	}
	service := auth.NewService(f.database, auth.Config{}, nil)
	if _, err = service.Login(t.Context(), httptest.NewRecorder(), httptest.NewRequest("POST", "http://localhost/login", nil), "admin", newPassword); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Login(t.Context(), httptest.NewRecorder(), httptest.NewRequest("POST", "http://localhost/login", nil), "admin", oldPassword); err == nil {
		t.Fatal("old password logs in")
	}
}

func TestResetRefusesIneligibleAccountBeforeReading(t *testing.T) {
	for _, tc := range []struct{ name, setup, user string }{
		{"missing", "", "missing"}, {"viewer", `UPDATE users SET role='viewer' WHERE username='admin'`, "admin"}, {"operator", `UPDATE users SET role='operator' WHERE username='admin'`, "admin"}, {"disabled", `UPDATE users SET disabled=1 WHERE username='admin'`, "admin"}, {"no-password", `UPDATE users SET password_hash=NULL WHERE username='admin'`, "admin"}, {"linked-SSO", `INSERT INTO user_identities VALUES('https://idp.invalid','subject',1,'2026-10-17T00:00:00Z')`, "admin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.setup != "" {
				if _, err := f.database.Exec(tc.setup); err != nil {
					t.Fatal(err)
				}
			}
			var before sql.NullString
			if err := f.database.QueryRow(`SELECT password_hash FROM users WHERE id=1`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			r := &recordReader{}
			if err := Reset(t.Context(), f.dir, tc.user, r); err == nil {
				t.Fatal("accepted ineligible account")
			}
			if r.read {
				t.Fatal("read password for ineligible account")
			}
			var after sql.NullString
			f.database.QueryRow(`SELECT password_hash FROM users WHERE id=1`).Scan(&after)
			if before != after || scalar(t, f.database, `SELECT COUNT(*) FROM sessions WHERE user_id=1`) != 2 || scalar(t, f.database, `SELECT COUNT(*) FROM api_tokens WHERE user_id=1`) != 2 || scalar(t, f.database, `SELECT COUNT(*) FROM audit_log`) != 0 {
				t.Fatal("refusal changed account")
			}
		})
	}
}

type recordReader struct{ read bool }

func (r *recordReader) Read([]byte) (int, error) { r.read = true; return 0, io.EOF }

func TestResetRejectsStdinAndRedactsErrors(t *testing.T) {
	for _, input := range []string{"", "short", "\n", newPassword + "\ntrailing", newPassword + "\n\n", newPassword + "\r", newPassword + "\rinside", strings.Repeat("p", 129), strings.Repeat("p", 20000)} {
		t.Run("invalid", func(t *testing.T) {
			f := newFixture(t)
			err := Reset(t.Context(), f.dir, "admin", strings.NewReader(input))
			if err == nil {
				t.Fatal("accepted invalid password input")
			}
			if strings.Contains(err.Error(), newPassword) || strings.Contains(err.Error(), f.oldHash) {
				t.Fatal("secret in error")
			}
			unchanged(t, f)
		})
	}
}
func TestResetPasswordBoundaries(t *testing.T) {
	for _, input := range []string{strings.Repeat("x", 10), strings.Repeat("x", 128) + "\n", strings.Repeat("x", 128) + "\r\n"} {
		t.Run("valid", func(t *testing.T) {
			f := newFixture(t)
			if err := Reset(t.Context(), f.dir, "admin", strings.NewReader(input)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResetTransactionRollsBackEveryStep(t *testing.T) {
	for _, table := range []string{"users", "sessions", "api_tokens", "audit_log"} {
		t.Run(table, func(t *testing.T) {
			f := newFixture(t)
			event := "DELETE"
			if table == "users" {
				event = "UPDATE"
			}
			if table == "audit_log" {
				event = "INSERT"
			}
			if _, err := f.database.Exec(`CREATE TRIGGER fail_recovery BEFORE ` + event + ` ON ` + table + ` BEGIN SELECT RAISE(ABORT,'sensitive-fixture-error'); END`); err != nil {
				t.Fatal(err)
			}
			err := Reset(t.Context(), f.dir, "admin", strings.NewReader(newPassword))
			if err == nil {
				t.Fatal("expected transaction failure")
			}
			if strings.Contains(err.Error(), "sensitive-fixture-error") {
				t.Fatal("database details leaked")
			}
			unchanged(t, f)
		})
	}
}
func TestResetRequiresManagerLockBeforeDatabaseOrInput(t *testing.T) {
	f := newFixture(t)
	release, err := launcher.AcquireManager(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	r := &recordReader{}
	err = Reset(t.Context(), f.dir, "admin", r)
	if err == nil || !strings.Contains(err.Error(), "manager") {
		t.Fatalf("missing lock refusal: %v", err)
	}
	if r.read {
		t.Fatal("read password despite lock")
	}
	unchanged(t, f)
}
func TestResetCancelled(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Reset(ctx, f.dir, "admin", strings.NewReader(newPassword)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reset: %v", err)
	}
	unchanged(t, f)
	ctx, cancel = context.WithCancel(t.Context())
	if err := Reset(ctx, f.dir, "admin", cancelReader{cancel}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled input reset: %v", err)
	}
	unchanged(t, f)
}

type cancelReader struct{ cancel context.CancelFunc }

func (r cancelReader) Read(p []byte) (int, error) { r.cancel(); return copy(p, newPassword), io.EOF }

func TestResetNeverCreatesOrMigrates(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if err := Reset(t.Context(), missing, "admin", strings.NewReader(newPassword)); err == nil {
		t.Fatal("accepted missing directory")
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatal("created directory")
	}
	dir := t.TempDir()
	if err := Reset(t.Context(), dir, "admin", strings.NewReader(newPassword)); err == nil {
		t.Fatal("accepted missing database")
	}
	if _, err := os.Lstat(filepath.Join(dir, "manager.db")); !os.IsNotExist(err) {
		t.Fatal("created database")
	}
	f := newFixture(t)
	if _, err := f.database.Exec(`UPDATE goose_db_version SET version_id=9999 WHERE id=(SELECT MAX(id) FROM goose_db_version)`); err != nil {
		t.Fatal(err)
	}
	if err := Reset(t.Context(), f.dir, "admin", strings.NewReader(newPassword)); err == nil {
		t.Fatal("accepted incompatible schema")
	}
	unchanged(t, f)
	if scalar(t, f.database, `SELECT MAX(version_id) FROM goose_db_version`) != 9999 {
		t.Fatal("schema changed")
	}
}
func TestResetRejectsUnsafePaths(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"relative", filepath.Join(f.dir, "manager.db")} {
		if err := Reset(t.Context(), path, "admin", strings.NewReader(newPassword)); err == nil {
			t.Fatal("accepted non-directory path")
		}
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(f.dir, link); err != nil {
		t.Skip("symlink unavailable", err)
	}
	if err := Reset(t.Context(), link, "admin", strings.NewReader(newPassword)); err == nil {
		t.Fatal("accepted symlink directory")
	}
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(f.dir, "manager.db"), filepath.Join(dir, "manager.db")); err != nil {
		t.Fatal(err)
	}
	if err := Reset(t.Context(), dir, "admin", strings.NewReader(newPassword)); err == nil {
		t.Fatal("accepted symlink database")
	}
	unchanged(t, f)
}

func TestResetHoldsLockUntilDatabaseClosed(t *testing.T) {
	f := newFixture(t)
	reader := &gateReader{started: make(chan struct{}), proceed: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- Reset(context.Background(), f.dir, "admin", reader) }()
	<-reader.started
	release, err := launcher.AcquireManager(f.dir)
	if err == nil {
		release()
		close(reader.proceed)
		<-done
		t.Fatal("recovery released manager lock while reading password")
	}
	close(reader.proceed)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	release, err = launcher.AcquireManager(f.dir)
	if err != nil {
		t.Fatal("recovery leaked manager lock", err)
	}
	release()
}

type gateReader struct {
	started, proceed chan struct{}
	sent             bool
}

func (r *gateReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.EOF
	}
	r.sent = true
	close(r.started)
	<-r.proceed
	return copy(p, newPassword), io.EOF
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("sensitive-reader-error") }
func TestResetDoesNotExposeReaderErrors(t *testing.T) {
	f := newFixture(t)
	err := Reset(t.Context(), f.dir, "admin", failingReader{})
	if err == nil || strings.Contains(err.Error(), "sensitive-reader-error") {
		t.Fatalf("unsafe reader error: %v", err)
	}
	unchanged(t, f)
}

func TestResetRejectsSymlinkSidecarsBeforeInput(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			f := newFixture(t)
			if err := f.database.Close(); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "private-fixture")
			if err := os.WriteFile(target, []byte("unchanged fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.dir, "manager.db") + suffix
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skip("symlink unavailable", err)
			}
			r := &recordReader{}
			if err := Reset(t.Context(), f.dir, "admin", r); err == nil {
				t.Fatal("accepted symlink SQLite sidecar")
			}
			if r.read {
				t.Fatal("read password for unsafe path")
			}
			content, err := os.ReadFile(target)
			if err != nil || string(content) != "unchanged fixture" {
				t.Fatal("sidecar symlink target changed")
			}
		})
	}
}

func TestResetRejectsSchemaWithoutMigration(t *testing.T) {
	for _, version := range []int64{1, 0} {
		t.Run("old-schema", func(t *testing.T) {
			f := newFixture(t)
			if _, err := f.database.Exec(`UPDATE goose_db_version SET version_id=? WHERE id=(SELECT MAX(id) FROM goose_db_version)`, version); err != nil {
				t.Fatal(err)
			}
			before := scalar(t, f.database, `SELECT COUNT(*) FROM goose_db_version`)
			if err := Reset(t.Context(), f.dir, "admin", strings.NewReader(newPassword)); err == nil {
				t.Fatal("accepted old schema")
			}
			if scalar(t, f.database, `SELECT COUNT(*) FROM goose_db_version`) != before {
				t.Fatal("schema migrated")
			}
			unchanged(t, f)
		})
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "manager.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Reset(t.Context(), dir, "admin", strings.NewReader(newPassword)); err == nil {
		t.Fatal("accepted empty DB")
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != 0 {
		t.Fatal("empty DB was initialized")
	}
}

func TestResetRefusesDisabledLocalLoginAndPreservesApplicationData(t *testing.T) {
	f := newFixture(t)
	settings := domain.DefaultSettings()
	settings.Auth.LocalLoginEnabled = false
	if err := db.NewSettingsRepo(f.database).Put(t.Context(), settings); err != nil {
		t.Fatal(err)
	}
	if _, err := f.database.Exec(`INSERT INTO jobs(id,kind,state,phase,created_at) VALUES('fixture-job','fixture','queued','waiting','2026-10-17T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := f.database.QueryRow(`SELECT doc FROM settings WHERE id=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := Reset(t.Context(), f.dir, "admin", strings.NewReader(newPassword)); err == nil {
		t.Fatal("accepted recovery with global local login disabled")
	}
	unchanged(t, f)
	var after string
	if err := f.database.QueryRow(`SELECT doc FROM settings WHERE id=1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after || scalar(t, f.database, `SELECT COUNT(*) FROM jobs WHERE id='fixture-job' AND state='queued'`) != 1 {
		t.Fatal("recovery changed settings or jobs")
	}
	service := auth.NewService(f.database, auth.Config{}, nil)
	if _, err := service.Login(t.Context(), httptest.NewRecorder(), httptest.NewRequest("POST", "http://localhost/login", nil), "admin", newPassword); err == nil {
		t.Fatal("recovery bypassed disabled local login")
	}
}

// Removing the stdin limit must fail before an unbounded input can be consumed.
func TestResetBoundsBytesConsumedFromStdin(t *testing.T) {
	f := newFixture(t)
	input := &countedReader{Reader: strings.NewReader(strings.Repeat("x", 10000))}
	if err := Reset(t.Context(), f.dir, "admin", input); err == nil {
		t.Fatal("accepted oversized stdin")
	}
	if input.count > 131 {
		t.Fatalf("read beyond bounded password input: %d", input.count)
	}
	unchanged(t, f)
}

type countedReader struct {
	io.Reader
	count int
}

func (r *countedReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.count += n
	return n, err
}
