package managerstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"modernc.org/sqlite"
)

const holdLimit = 4096

type MigrationOptions struct {
	SnapshotID           string
	FromSchema, ToSchema int64
}

type MaintenanceSummary struct {
	Format           int    `json:"format"`
	Operation        string `json:"operation"`
	State            string `json:"state"`
	SnapshotID       string `json:"snapshot_id"`
	BeforeSchema     int64  `json:"before_schema"`
	AfterSchema      int64  `json:"after_schema"`
	ExecutableSHA256 string `json:"executable_sha256"`
	ReviewRequired   bool   `json:"review_required"`
}

type migrationHold struct {
	Format                 int    `json:"format"`
	Operation              string `json:"operation"`
	Phase                  string `json:"phase"`
	SnapshotID             string `json:"snapshot_id"`
	SnapshotManifestSHA256 string `json:"snapshot_manifest_sha256"`
	FromSchema             int64  `json:"from_schema"`
	ToSchema               int64  `json:"to_schema"`
	BinaryVersion          string `json:"binary_version"`
	ExecutableSHA256       string `json:"executable_sha256"`
}

type maintenancePin struct {
	file *os.File
	info os.FileInfo
	raw  []byte
}

type lockedMaintenance struct {
	data        *os.Root
	maintenance *os.Root
	dataDir     string
	managerSID  string
	release     func()
}

type maintenanceDeps struct {
	authorize          func() error
	imageHash          func(context.Context) (string, error)
	fence              func(context.Context, *os.Root, *os.Root) error
	migrate            func(context.Context, *sql.DB) error
	afterHold          func() error
	beforeDatabasePing func() error
	afterDatabasePing  func() error
	afterDriverConnect func() error
}

type maintenanceError struct {
	message string
	cause   error
}

func (e *maintenanceError) Error() string { return e.message }
func (e *maintenanceError) Unwrap() error { return e.cause }

func maintenanceFailure(message string, err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return &maintenanceError{message: message, cause: context.Canceled}
	case errors.Is(err, context.DeadlineExceeded):
		return &maintenanceError{message: message, cause: context.DeadlineExceeded}
	default:
		return &maintenanceError{message: message}
	}
}

func (s *lockedMaintenance) close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	if s.maintenance != nil {
		closeErr = errors.Join(closeErr, s.maintenance.Close())
		s.maintenance = nil
	}
	if s.data != nil {
		closeErr = errors.Join(closeErr, s.data.Close())
		s.data = nil
	}
	if s.release != nil {
		s.release()
		s.release = nil
	}
	return closeErr
}

func acquireLockedMaintenance(ctx context.Context, dataDir string, authorize func() error) (*lockedMaintenance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if authorize != nil {
		if err := authorize(); err != nil {
			return nil, errors.New("administrator authority is required")
		}
	}
	data, err := openDataRoot(ctx, dataDir)
	if err != nil {
		return nil, err
	}
	closeData := func() { _ = data.Close() }
	if err := verifyDataBinding(ctx, dataDir, data); err != nil {
		closeData()
		return nil, err
	}
	release, err := launcher.AcquireManager(dataDir)
	if err != nil {
		closeData()
		return nil, errors.New("manager state is busy")
	}
	session := &lockedMaintenance{data: data, dataDir: dataDir, release: release}
	if err := verifyDataBinding(ctx, dataDir, data); err != nil {
		_ = session.close()
		return nil, err
	}
	if _, err := sourceFiles(data); err != nil {
		_ = session.close()
		return nil, err
	}
	managerSID, err := platformManagerSID(data)
	if err != nil {
		_ = session.close()
		return nil, errors.New("manager account identity could not be verified")
	}
	session.managerSID = managerSID
	maintenance, err := openPrivateDirectory(data, "maintenance", managerSID)
	if err != nil {
		_ = session.close()
		return nil, errors.New("private maintenance storage is unavailable")
	}
	session.maintenance = maintenance
	if err := verifyMaintenanceRootBinding(ctx, dataDir, data, maintenance); err != nil {
		_ = session.close()
		return nil, errors.New("maintenance directory binding changed")
	}
	return session, nil
}

// CheckStartup is read-only. Callers hold the normal manager data lock and
// invoke it before opening SQLite.
func CheckStartup(ctx context.Context, dataDir string) error {
	if err := ctx.Err(); err != nil {
		return maintenanceFailure("startup is blocked by maintenance state", err)
	}
	if !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) != dataDir {
		return maintenanceFailure("manager startup could not inspect maintenance state", nil)
	}
	data, err := safepath.OpenRoot(ctx, dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return maintenanceFailure("manager startup could not inspect maintenance state", err)
	}
	defer data.Close()
	if err := verifyDataBinding(ctx, dataDir, data); err != nil {
		return maintenanceFailure("manager startup could not inspect maintenance state", err)
	}
	info, err := data.Lstat("maintenance")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info == nil || !info.IsDir() {
		return maintenanceFailure("manager startup could not inspect maintenance state", err)
	}
	managerSID, err := platformManagerSID(data)
	if err != nil {
		return maintenanceFailure("manager startup could not verify maintenance storage", err)
	}
	maintenance, err := openPrivateDirectory(data, "maintenance", managerSID)
	if err != nil {
		return maintenanceFailure("manager startup could not verify maintenance storage", err)
	}
	defer maintenance.Close()
	if err := verifyMaintenanceRootBinding(ctx, dataDir, data, maintenance); err != nil {
		return maintenanceFailure("manager startup could not verify maintenance storage", err)
	}
	for _, name := range []string{"hold.json", "hold.pending"} {
		if _, err := maintenance.Lstat(name); err == nil {
			return maintenanceFailure("manager startup is blocked; maintenance review is required", nil)
		} else if !errors.Is(err, os.ErrNotExist) {
			return maintenanceFailure("manager startup could not inspect maintenance state", err)
		}
	}
	return ctx.Err()
}

func verifyMaintenanceRootBinding(ctx context.Context, dataDir string, data, maintenance *os.Root) error {
	if err := verifyDataBinding(ctx, dataDir, data); err != nil {
		return err
	}
	info, err := data.Lstat("maintenance")
	opened, openErr := maintenance.Stat(".")
	current, pathErr := os.Lstat(filepath.Join(dataDir, "maintenance"))
	if err != nil || openErr != nil || pathErr != nil ||
		!os.SameFile(info, opened) || !os.SameFile(info, current) {
		return errors.New("maintenance directory binding changed")
	}
	return nil
}

// Migrate applies the embedded migrations to an existing database under the
// manager lock and leaves a durable startup hold until explicit Resume.
func Migrate(ctx context.Context, dataDir, binaryVersion string, options MigrationOptions) (MaintenanceSummary, error) {
	return migrateWithDeps(ctx, dataDir, binaryVersion, options, productionMaintenanceDeps())
}

// Resume removes a matching migrated hold only after explicit confirmation and
// complete database and snapshot revalidation.
func Resume(ctx context.Context, dataDir, binaryVersion, snapshotID string, confirmed bool) (MaintenanceSummary, error) {
	return resumeWithDeps(ctx, dataDir, binaryVersion, snapshotID, confirmed, productionMaintenanceDeps())
}

func migrateWithDeps(ctx context.Context, dataDir, binaryVersion string, options MigrationOptions, deps maintenanceDeps) (summary MaintenanceSummary, retErr error) {
	if deps.imageHash == nil || deps.fence == nil {
		return summary, maintenanceFailure("migration options are invalid", nil)
	}
	if err := ctx.Err(); err != nil {
		return summary, maintenanceFailure("migration requires review or recovery", err)
	}
	if !validID(options.SnapshotID) || validateVersion(binaryVersion) != nil ||
		options.FromSchema < 1 || options.FromSchema >= options.ToSchema || options.ToSchema != db.LatestVersion() {
		return summary, maintenanceFailure("migration options are invalid", nil)
	}
	session, err := acquireLockedMaintenance(ctx, dataDir, deps.authorize)
	if err != nil {
		return summary, maintenanceFailure("migration could not acquire safe manager state", err)
	}
	defer func() {
		if closeErr := session.close(); closeErr != nil {
			retErr = errors.Join(retErr, maintenanceFailure("migration requires review or recovery", closeErr))
			summary = MaintenanceSummary{}
		}
	}()
	if err := ensureNoHold(session.maintenance); err != nil {
		return summary, maintenanceFailure("migration requires review or recovery", err)
	}
	imageHash, err := deps.imageHash(ctx)
	if err != nil || !validDigest(imageHash) {
		return summary, maintenanceFailure("running manager image could not be verified", err)
	}
	snapshotSummary, records, err := verifySnapshotRecords(ctx, session, options.SnapshotID)
	if err != nil || snapshotSummary.Schema != options.FromSchema {
		return summary, maintenanceFailure("snapshot does not match the requested source schema", err)
	}
	if err := compareCurrentToSnapshot(ctx, session.data, records); err != nil {
		return summary, maintenanceFailure("current database differs from the verified snapshot", err)
	}
	if err := verifyDataBinding(ctx, dataDir, session.data); err != nil {
		return summary, maintenanceFailure("manager data binding changed before migration", err)
	}
	currentFiles, err := sourceFiles(session.data)
	if err != nil {
		return summary, maintenanceFailure("manager database files are unsafe", err)
	}
	if err := verifySourceSet(ctx, session.data, currentFiles, records); err != nil {
		return summary, maintenanceFailure("manager database changed before migration", err)
	}
	if err := verifyMaintenanceRootBinding(ctx, dataDir, session.data, session.maintenance); err != nil {
		return summary, maintenanceFailure("maintenance directory binding changed before migration", err)
	}
	hold := migrationHold{
		Format: 1, Operation: "migrate", Phase: "migrating",
		SnapshotID: options.SnapshotID, SnapshotManifestSHA256: snapshotSummary.ManifestSHA256,
		FromSchema: options.FromSchema, ToSchema: options.ToSchema,
		BinaryVersion: binaryVersion, ExecutableSHA256: imageHash,
	}
	pin, err := publishInitialHold(ctx, session, hold, deps.fence)
	if err != nil {
		return summary, maintenanceFailure("migration hold publication is uncertain; review or recovery is required", err)
	}
	defer func() {
		if pin != nil && pin.file != nil {
			if closeErr := pin.file.Close(); closeErr != nil {
				retErr = errors.Join(retErr, maintenanceFailure("migration requires review or recovery", closeErr))
				summary = MaintenanceSummary{}
			}
		}
	}()
	if deps.afterHold != nil {
		if err := deps.afterHold(); err != nil {
			return summary, maintenanceFailure("migration stopped after publishing its recovery hold", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return summary, maintenanceFailure("migration stopped after publishing its recovery hold", err)
	}
	if err := verifyDataBinding(ctx, dataDir, session.data); err != nil {
		return summary, maintenanceFailure("migration requires review or recovery", err)
	}
	if err := verifyMaintenanceRootBinding(ctx, dataDir, session.data, session.maintenance); err != nil {
		return summary, maintenanceFailure("migration requires review or recovery", err)
	}
	if err := verifyHoldPin(ctx, session.maintenance, "hold.json", pin); err != nil {
		return summary, maintenanceFailure("migration hold changed; review or recovery is required", err)
	}
	if err := verifySourceSet(ctx, session.data, currentFiles, records); err != nil {
		return summary, maintenanceFailure("manager database changed before migration", err)
	}
	databaseInfo, err := session.data.Lstat("manager.db")
	if err != nil {
		return summary, maintenanceFailure("existing manager database is unavailable", err)
	}
	database, err := openMigrationDatabase(ctx, dataDir, session.data, databaseInfo, deps.beforeDatabasePing, deps.afterDatabasePing, deps.afterDriverConnect)
	if err != nil {
		return summary, maintenanceFailure("manager database could not be safely opened for migration", err)
	}
	closeDatabase := func() error { return closeMigrationDatabase(ctx, dataDir, session.data, database) }
	version, err := db.CurrentVersion(ctx, database)
	if err != nil || version != options.FromSchema {
		_ = closeDatabase()
		return summary, maintenanceFailure("live schema differs from the verified snapshot", err)
	}
	if err := verifyMigrationPragmas(ctx, database); err != nil {
		_ = closeDatabase()
		return summary, maintenanceFailure("required SQLite durability settings could not be verified", err)
	}
	migrate := deps.migrate
	if migrate == nil {
		migrate = db.Migrate
	}
	if err := migrate(ctx, database); err != nil {
		_ = closeDatabase()
		return summary, maintenanceFailure("migration stopped with recovery required", err)
	}
	if err := ctx.Err(); err != nil {
		_ = closeDatabase()
		return summary, maintenanceFailure("migration stopped with recovery required", err)
	}
	version, err = validateSQLiteHandle(ctx, database)
	if err != nil || version != options.ToSchema {
		_ = closeDatabase()
		return summary, maintenanceFailure("migrated database failed integrity or schema validation", err)
	}
	if err := verifyOpenDatabaseIdentity(session.data, databaseInfo); err != nil {
		_ = closeDatabase()
		return summary, maintenanceFailure("manager database identity changed during migration", err)
	}
	if err := closeDatabase(); err != nil {
		return summary, maintenanceFailure("database close failed; recovery is required", err)
	}
	if err := verifyOpenDatabaseIdentity(session.data, databaseInfo); err != nil {
		return summary, maintenanceFailure("manager database identity changed after migration", err)
	}
	if _, err := sourceFiles(session.data); err != nil {
		return summary, maintenanceFailure("migrated database files are unsafe", err)
	}
	if err := deps.fence(ctx, session.data, session.maintenance); err != nil {
		return summary, maintenanceFailure("migrated database durability is uncertain; review or recovery is required", err)
	}
	hold.Phase = "migrated"
	newPin, err := replaceHoldCAS(ctx, session, pin, hold, deps.fence)
	if err != nil {
		return summary, maintenanceFailure("migration completed with an uncertain hold; review or recovery is required", err)
	}
	if err := pin.file.Close(); err != nil {
		_ = newPin.file.Close()
		return summary, maintenanceFailure("migration completed with an uncertain hold; review or recovery is required", err)
	}
	pin = newPin
	return MaintenanceSummary{
		Format: 1, Operation: "migrate", State: "migrated", SnapshotID: hold.SnapshotID,
		BeforeSchema: hold.FromSchema, AfterSchema: hold.ToSchema,
		ExecutableSHA256: hold.ExecutableSHA256, ReviewRequired: true,
	}, nil
}

func resumeWithDeps(ctx context.Context, dataDir, binaryVersion, snapshotID string, confirmed bool, deps maintenanceDeps) (summary MaintenanceSummary, retErr error) {
	if deps.imageHash == nil || deps.fence == nil {
		return summary, maintenanceFailure("resume options are invalid", nil)
	}
	if !confirmed {
		return summary, maintenanceFailure("explicit maintenance confirmation is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return summary, maintenanceFailure("resume requires review or recovery", err)
	}
	if !validID(snapshotID) || validateVersion(binaryVersion) != nil {
		return summary, maintenanceFailure("resume options are invalid", nil)
	}
	session, err := acquireLockedMaintenance(ctx, dataDir, deps.authorize)
	if err != nil {
		return summary, maintenanceFailure("resume could not acquire safe manager state", err)
	}
	defer func() {
		if closeErr := session.close(); closeErr != nil {
			retErr = errors.Join(retErr, maintenanceFailure("resume requires review or recovery", closeErr))
			summary = MaintenanceSummary{}
		}
	}()
	if err := requireAbsent(session.maintenance, "hold.pending"); err != nil {
		return summary, maintenanceFailure("resume found pending maintenance state", err)
	}
	pin, hold, err := openCurrentHold(ctx, session.maintenance)
	if err != nil {
		return summary, maintenanceFailure("resume requires a matching migrated hold", err)
	}
	defer func() {
		if pin != nil && pin.file != nil {
			if closeErr := pin.file.Close(); closeErr != nil {
				retErr = errors.Join(retErr, maintenanceFailure("resume removal is uncertain", closeErr))
				summary = MaintenanceSummary{}
			}
		}
	}()
	if hold.Phase != "migrated" || hold.SnapshotID != snapshotID ||
		hold.BinaryVersion != binaryVersion || hold.ToSchema != db.LatestVersion() {
		return summary, maintenanceFailure("resume does not match the migrated maintenance hold", nil)
	}
	imageHash, err := deps.imageHash(ctx)
	if err != nil || imageHash != hold.ExecutableSHA256 {
		return summary, maintenanceFailure("current manager image does not match the migrated hold", err)
	}
	snapshotSummary, _, err := verifySnapshotRecords(ctx, session, snapshotID)
	if err != nil || snapshotSummary.ManifestSHA256 != hold.SnapshotManifestSHA256 || snapshotSummary.Schema != hold.FromSchema {
		return summary, maintenanceFailure("referenced recovery snapshot failed verification", err)
	}
	if _, err := sourceFiles(session.data); err != nil {
		return summary, maintenanceFailure("current manager database files are unsafe", err)
	}
	databaseInfo, err := session.data.Lstat("manager.db")
	if err != nil {
		return summary, maintenanceFailure("current manager database is unavailable", err)
	}
	database, err := openMigrationDatabase(ctx, dataDir, session.data, databaseInfo, deps.beforeDatabasePing, deps.afterDatabasePing, deps.afterDriverConnect)
	if err != nil {
		return summary, maintenanceFailure("current manager database could not be safely verified", err)
	}
	closeDatabase := func() error { return closeMigrationDatabase(ctx, dataDir, session.data, database) }
	version, err := validateSQLiteHandle(ctx, database)
	if err != nil || version != hold.ToSchema {
		_ = closeDatabase()
		return summary, maintenanceFailure("current manager database requires review or recovery", err)
	}
	if err := verifyMigrationPragmas(ctx, database); err != nil {
		_ = closeDatabase()
		return summary, maintenanceFailure("required SQLite durability settings could not be verified", err)
	}
	if err := verifyOpenDatabaseIdentity(session.data, databaseInfo); err != nil {
		_ = closeDatabase()
		return summary, maintenanceFailure("manager database identity changed during resume", err)
	}
	if err := closeDatabase(); err != nil {
		return summary, maintenanceFailure("database close failed; resume is uncertain", err)
	}
	if err := verifyOpenDatabaseIdentity(session.data, databaseInfo); err != nil {
		return summary, maintenanceFailure("manager database identity changed after resume", err)
	}
	if _, err := sourceFiles(session.data); err != nil {
		return summary, maintenanceFailure("current manager database files are unsafe", err)
	}
	if err := verifyDataBinding(ctx, dataDir, session.data); err != nil {
		return summary, maintenanceFailure("manager data binding changed before resume", err)
	}
	if err := verifyMaintenanceRootBinding(ctx, dataDir, session.data, session.maintenance); err != nil {
		return summary, maintenanceFailure("maintenance directory binding changed before resume", err)
	}
	if err := verifyHoldPin(ctx, session.maintenance, "hold.json", pin); err != nil {
		return summary, maintenanceFailure("maintenance hold changed before removal", err)
	}
	if err := requireAbsent(session.maintenance, "hold.pending"); err != nil {
		return summary, maintenanceFailure("resume found pending maintenance state", err)
	}
	if err := session.maintenance.Remove("hold.json"); err != nil {
		return summary, maintenanceFailure("maintenance hold removal is uncertain", err)
	}
	if err := pin.file.Close(); err != nil {
		pin.file = nil
		return summary, maintenanceFailure("maintenance hold removal is uncertain", err)
	}
	pin.file = nil
	if err := deps.fence(ctx, session.data, session.maintenance); err != nil {
		return summary, maintenanceFailure("maintenance hold removal is uncertain; review or recovery is required", err)
	}
	return MaintenanceSummary{
		Format: 1, Operation: "migrate", State: "resumed", SnapshotID: hold.SnapshotID,
		BeforeSchema: hold.FromSchema, AfterSchema: hold.ToSchema,
		ExecutableSHA256: hold.ExecutableSHA256, ReviewRequired: false,
	}, nil
}

func productionMaintenanceDeps() maintenanceDeps {
	return maintenanceDeps{authorize: authorizeMaintenance, imageHash: runningImageHash, fence: fenceMaintenance}
}

func ensureNoHold(root *os.Root) error {
	for _, name := range []string{"hold.json", "hold.pending"} {
		if err := requireAbsent(root, name); err != nil {
			return errors.New("maintenance state already exists")
		}
	}
	return nil
}

func requireAbsent(root *os.Root, name string) error {
	if _, err := root.Lstat(name); err == nil {
		return errors.New("maintenance state already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func verifySnapshotRecords(ctx context.Context, session *lockedMaintenance, id string) (Summary, []fileRecord, error) {
	maintenancePath := filepath.Join(session.dataDir, "maintenance")
	summary, err := verifySnapshot(ctx, session.dataDir, session.data, session.maintenance, id, maintenancePath, session.managerSID)
	if err != nil {
		return Summary{}, nil, err
	}
	snapshot, err := openCheckedDirectory(session.maintenance, id, session.managerSID)
	if err != nil {
		return Summary{}, nil, errors.New("snapshot directory changed after verification")
	}
	defer snapshot.Close()
	metadata, body, err := readManifest(snapshot)
	if err != nil || !validManifest(metadata) || metadata.ID != id {
		return Summary{}, nil, errors.New("snapshot manifest changed after verification")
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != summary.ManifestSHA256 {
		return Summary{}, nil, errors.New("snapshot manifest digest changed after verification")
	}
	return summary, append([]fileRecord(nil), metadata.Files...), nil
}

func compareCurrentToSnapshot(ctx context.Context, root *os.Root, records []fileRecord) error {
	current, err := sourceFiles(root)
	if err != nil || len(current) != len(records) {
		return errors.New("database sidecar file set differs from snapshot")
	}
	for i, expected := range records {
		if current[i].name != expected.Name || current[i].info.Size() != expected.Size {
			return errors.New("database sidecar file set differs from snapshot")
		}
		actual, err := copyToHash(ctx, root, expected.Name, expected.Size)
		if err != nil || actual.SHA256 != expected.SHA256 {
			return errors.New("database bytes differ from snapshot")
		}
	}
	return verifySourceSet(ctx, root, current, records)
}

func openMigrationDatabase(ctx context.Context, dataDir string, data *os.Root, expected os.FileInfo, beforePing, afterPing, afterDriverConnect func() error) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := verifyDataBinding(ctx, dataDir, data); err != nil {
		return nil, errors.New("manager data directory changed before database open")
	}
	if err := verifyOpenDatabaseIdentity(data, expected); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "manager.db")
	base, err := sqlite.NewConnector(db.FileURI(path) + "?mode=rw")
	if err != nil {
		return nil, errors.New("existing database could not be opened")
	}
	database := sql.OpenDB(migrationConnector{
		Connector: base, dataDir: dataDir, data: data, expected: expected,
		afterConnect: afterDriverConnect,
	})
	database.SetMaxOpenConns(1)
	if beforePing != nil {
		if err := beforePing(); err != nil {
			_ = database.Close()
			return nil, errors.New("existing database connection could not be verified")
		}
	}
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, errors.New("existing database connection failed")
	}
	if afterPing != nil {
		if err := afterPing(); err != nil {
			_ = database.Close()
			return nil, errors.New("existing database connection could not be verified")
		}
	}
	if err := verifyDataBinding(ctx, dataDir, data); err != nil {
		_ = database.Close()
		return nil, errors.New("manager data directory changed after database open")
	}
	if err := verifyOpenDatabaseIdentity(data, expected); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

type migrationConnector struct {
	driver.Connector
	dataDir      string
	data         *os.Root
	expected     os.FileInfo
	afterConnect func() error
}

func (c migrationConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := verifyDataBinding(ctx, c.dataDir, c.data); err != nil {
		return nil, errors.New("manager data directory changed before SQLite connection")
	}
	if err := verifyOpenDatabaseIdentity(c.data, c.expected); err != nil {
		return nil, err
	}
	connection, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, errors.New("existing database connection failed")
	}
	closeWithError := func(connectErr error) (driver.Conn, error) {
		return nil, errors.Join(connectErr, connection.Close())
	}
	if c.afterConnect != nil {
		if err := c.afterConnect(); err != nil {
			return closeWithError(errors.New("SQLite connection binding could not be verified"))
		}
	}
	if err := verifyDataBinding(ctx, c.dataDir, c.data); err != nil {
		return closeWithError(errors.New("manager data directory changed during SQLite connection"))
	}
	if err := verifyOpenDatabaseIdentity(c.data, c.expected); err != nil {
		return closeWithError(err)
	}
	if err := configureMigrationConnection(ctx, connection); err != nil {
		return closeWithError(err)
	}
	if err := verifyDataBinding(ctx, c.dataDir, c.data); err != nil {
		return closeWithError(errors.New("manager data directory changed after SQLite settings"))
	}
	if err := verifyOpenDatabaseIdentity(c.data, c.expected); err != nil {
		return closeWithError(err)
	}
	return connection, nil
}

func (c migrationConnector) Driver() driver.Driver { return c.Connector.Driver() }

func configureMigrationConnection(ctx context.Context, connection driver.Conn) error {
	queryer, ok := connection.(driver.QueryerContext)
	if !ok {
		return errors.New("SQLite connection cannot verify required settings")
	}
	execer, ok := connection.(driver.ExecerContext)
	if !ok {
		return errors.New("SQLite connection cannot apply required settings")
	}
	journal, err := readMigrationPragma(ctx, queryer, "journal_mode=WAL")
	if err != nil || journal != "wal" {
		return errors.New("SQLite WAL mode is unavailable")
	}
	for _, statement := range []string{
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=FULL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := execer.ExecContext(ctx, statement, nil); err != nil {
			return errors.New("SQLite durability settings are unavailable")
		}
	}
	for _, setting := range []struct {
		name string
		want driver.Value
	}{
		{"journal_mode", "wal"},
		{"foreign_keys", int64(1)},
		{"synchronous", int64(2)},
		{"busy_timeout", int64(5000)},
	} {
		got, err := readMigrationPragma(ctx, queryer, setting.name)
		if err != nil || !reflect.DeepEqual(got, setting.want) {
			return errors.New("SQLite connection settings do not match required durability")
		}
	}
	return ctx.Err()
}

func readMigrationPragma(ctx context.Context, queryer driver.QueryerContext, name string) (value driver.Value, retErr error) {
	rows, err := queryer.QueryContext(ctx, "PRAGMA "+name, nil)
	if err != nil {
		return nil, err
	}
	defer func() { retErr = errors.Join(retErr, rows.Close()) }()
	if len(rows.Columns()) != 1 {
		return nil, errors.New("SQLite pragma returned unexpected columns")
	}
	values := make([]driver.Value, 1)
	if err := rows.Next(values); err != nil {
		return nil, err
	}
	value = values[0]
	if err := rows.Next(values); err != io.EOF {
		if err == nil {
			return nil, errors.New("SQLite pragma returned unexpected rows")
		}
		return nil, err
	}
	return value, nil
}

func closeMigrationDatabase(ctx context.Context, dataDir string, data *os.Root, database *sql.DB) error {
	closeErr := database.Close()
	bindingErr := verifyDataBinding(ctx, dataDir, data)
	if closeErr != nil || bindingErr != nil {
		return errors.New("database close or data directory binding check failed")
	}
	return nil
}

func verifyOpenDatabaseIdentity(data *os.Root, expected os.FileInfo) error {
	if expected == nil {
		return errors.New("database identity is unavailable")
	}
	current, err := data.Lstat("manager.db")
	if err != nil || !os.SameFile(expected, current) || !current.Mode().IsRegular() {
		return errors.New("database identity changed")
	}
	return nil
}

func verifyMigrationPragmas(ctx context.Context, database *sql.DB) error {
	var journal string
	var foreignKeys, synchronous, busyTimeout int64
	if err := database.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
		return errors.New("SQLite WAL mode is unavailable")
	}
	if err := database.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		return errors.New("SQLite foreign key enforcement is unavailable")
	}
	if err := database.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
		return errors.New("SQLite full synchronous mode is unavailable")
	}
	if err := database.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil || busyTimeout != 5000 {
		return errors.New("SQLite busy timeout is unavailable")
	}
	return ctx.Err()
}

func openCurrentHold(ctx context.Context, root *os.Root) (*maintenancePin, migrationHold, error) {
	var hold migrationHold
	if err := requireAbsent(root, "hold.pending"); err != nil {
		return nil, hold, err
	}
	info, err := root.Lstat("hold.json")
	if err != nil || !info.Mode().IsRegular() || checkOrdinaryFile(root, "hold.json", info) != nil {
		return nil, hold, errors.New("maintenance hold is missing or unsafe")
	}
	file, err := root.Open("hold.json")
	if err != nil {
		return nil, hold, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, hold, errors.New("maintenance hold identity changed")
	}
	body, err := readMaintenanceFile(ctx, file, holdLimit)
	if err != nil {
		_ = file.Close()
		return nil, hold, err
	}
	if err := strictHoldJSON(body, &hold); err != nil || !validMigrationHold(hold) {
		_ = file.Close()
		return nil, hold, errors.New("maintenance hold is invalid")
	}
	after, err := file.Stat()
	current, pathErr := root.Lstat("hold.json")
	if err != nil || pathErr != nil || !sameFileState(info, after) || !os.SameFile(after, current) {
		_ = file.Close()
		return nil, hold, errors.New("maintenance hold changed while reading")
	}
	return &maintenancePin{file: file, info: info, raw: bytes.Clone(body)}, hold, nil
}

func validMigrationHold(hold migrationHold) bool {
	return hold.Format == 1 && hold.Operation == "migrate" &&
		(hold.Phase == "migrating" || hold.Phase == "migrated") &&
		validID(hold.SnapshotID) && validDigest(hold.SnapshotManifestSHA256) &&
		hold.FromSchema >= 1 && hold.FromSchema < hold.ToSchema &&
		hold.ToSchema == db.LatestVersion() && validateVersion(hold.BinaryVersion) == nil &&
		validDigest(hold.ExecutableSHA256)
}

func sameFileState(before, after os.FileInfo) bool {
	return before != nil && after != nil && os.SameFile(before, after) &&
		before.Size() == after.Size() && before.Mode() == after.Mode() &&
		before.ModTime().Equal(after.ModTime())
}

func readMaintenanceFile(ctx context.Context, file *os.File, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(maintenanceContextReader{ctx, io.NewSectionReader(file, 0, limit+1)}, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("maintenance file exceeds bound")
	}
	return body, nil
}

func verifyHoldPin(ctx context.Context, root *os.Root, name string, pin *maintenancePin) error {
	if pin == nil || pin.file == nil || pin.info == nil {
		return errors.New("maintenance hold pin is unavailable")
	}
	info, err := pin.file.Stat()
	if err != nil || !sameFileState(pin.info, info) || checkOpenFile(pin.file, pin.info) != nil {
		return errors.New("maintenance hold handle changed")
	}
	pathInfo, err := root.Lstat(name)
	if err != nil || !os.SameFile(pin.info, pathInfo) || checkOrdinaryFile(root, name, pathInfo) != nil {
		return errors.New("maintenance hold path changed")
	}
	raw, err := readMaintenanceFile(ctx, pin.file, holdLimit)
	if err != nil || !bytes.Equal(raw, pin.raw) {
		return errors.New("maintenance hold bytes changed")
	}
	after, err := pin.file.Stat()
	pathAfter, pathErr := root.Lstat(name)
	if err != nil || pathErr != nil || !sameFileState(pin.info, after) || !os.SameFile(after, pathAfter) {
		return errors.New("maintenance hold changed during recheck")
	}
	return nil
}

func marshalHold(hold migrationHold) ([]byte, error) {
	if !validMigrationHold(hold) {
		return nil, errors.New("maintenance hold fields are invalid")
	}
	body, err := json.Marshal(hold)
	if err != nil || len(body) == 0 || len(body) > holdLimit {
		return nil, errors.New("maintenance hold encoding failed")
	}
	return body, nil
}

func publishInitialHold(ctx context.Context, session *lockedMaintenance, hold migrationHold, fence func(context.Context, *os.Root, *os.Root) error) (*maintenancePin, error) {
	if err := ensureNoHold(session.maintenance); err != nil {
		return nil, err
	}
	body, err := marshalHold(hold)
	if err != nil {
		return nil, err
	}
	pin, err := createHoldPending(ctx, session.maintenance, body)
	if err != nil {
		return nil, err
	}
	if err := requireAbsent(session.maintenance, "hold.json"); err != nil {
		_ = pin.file.Close()
		return nil, err
	}
	if err := verifyHoldPin(ctx, session.maintenance, "hold.pending", pin); err != nil {
		_ = pin.file.Close()
		return nil, err
	}
	if err := session.maintenance.Rename("hold.pending", "hold.json"); err != nil {
		_ = pin.file.Close()
		return nil, err
	}
	pin.raw = bytes.Clone(body)
	if err := verifyHoldPin(ctx, session.maintenance, "hold.json", pin); err != nil {
		_ = pin.file.Close()
		return nil, err
	}
	if err := fence(ctx, session.data, session.maintenance); err != nil {
		_ = pin.file.Close()
		return nil, err
	}
	return pin, nil
}

func createHoldPending(ctx context.Context, root *os.Root, body []byte) (*maintenancePin, error) {
	file, err := root.OpenFile("hold.pending", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || checkOpenFile(file, info) != nil {
		_ = file.Close()
		return nil, errors.New("maintenance pending file is unsafe")
	}
	pin := &maintenancePin{file: file, info: info}
	for offset := 0; offset < len(body); {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		n, writeErr := file.Write(body[offset:])
		if writeErr != nil || n <= 0 || n > len(body)-offset {
			_ = file.Close()
			return nil, errors.New("maintenance pending write was incomplete")
		}
		offset += n
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, errors.New("maintenance pending file sync failed")
	}
	finalInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, finalInfo) || checkOpenFile(file, finalInfo) != nil {
		_ = file.Close()
		return nil, errors.New("maintenance pending file identity is unsafe")
	}
	pin.info = finalInfo
	current, err := root.Lstat("hold.pending")
	if err != nil || !os.SameFile(finalInfo, current) || checkOrdinaryFile(root, "hold.pending", current) != nil {
		_ = file.Close()
		return nil, errors.New("maintenance pending path identity changed")
	}
	pin.raw = bytes.Clone(body)
	return pin, nil
}

func replaceHoldCAS(ctx context.Context, session *lockedMaintenance, previous *maintenancePin, next migrationHold, fence func(context.Context, *os.Root, *os.Root) error) (*maintenancePin, error) {
	if err := verifyHoldPin(ctx, session.maintenance, "hold.json", previous); err != nil {
		return nil, err
	}
	if err := requireAbsent(session.maintenance, "hold.pending"); err != nil {
		return nil, err
	}
	body, err := marshalHold(next)
	if err != nil {
		return nil, err
	}
	pending, err := createHoldPending(ctx, session.maintenance, body)
	if err != nil {
		return nil, err
	}
	if err := verifyHoldPin(ctx, session.maintenance, "hold.json", previous); err != nil {
		_ = pending.file.Close()
		return nil, err
	}
	if err := verifyHoldPin(ctx, session.maintenance, "hold.pending", pending); err != nil {
		_ = pending.file.Close()
		return nil, err
	}
	if err := session.maintenance.Rename("hold.pending", "hold.json"); err != nil {
		_ = pending.file.Close()
		return nil, err
	}
	if err := verifyHoldPin(ctx, session.maintenance, "hold.json", pending); err != nil {
		_ = pending.file.Close()
		return nil, err
	}
	if err := fence(ctx, session.data, session.maintenance); err != nil {
		_ = pending.file.Close()
		return nil, err
	}
	return pending, nil
}

func strictHoldJSON(body []byte, hold *migrationHold) error {
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(hold); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("hold has trailing data")
	}
	return nil
}

func runningImageDigest(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("running manager image could not be opened")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > 1<<30 {
		return "", errors.New("running manager image is unsafe")
	}
	pathBefore, err := os.Stat(path)
	if err != nil || !os.SameFile(before, pathBefore) {
		return "", errors.New("running manager image identity changed")
	}
	hash := sha256.New()
	buffer := make([]byte, copyBufferSize)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			total += int64(n)
			if total > before.Size() {
				return "", errors.New("running manager image grew while hashing")
			}
			_, _ = hash.Write(buffer[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", errors.New("running manager image could not be hashed")
		}
	}
	after, err := file.Stat()
	pathAfter, pathErr := os.Stat(path)
	if err != nil || pathErr != nil || !sameFileState(before, after) ||
		!os.SameFile(after, pathAfter) || total != before.Size() {
		return "", errors.New("running manager image changed while hashing")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type maintenanceContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r maintenanceContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(buffer)
}
