package database

import (
	"database/sql"
	"fmt"
	"path"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// JoinPath joins path elements and ensures the result is a clean, absolute path starting with /.
func JoinPath(elem ...string) string {
	return path.Join("/", path.Join(elem...))
}

type File struct {
	ID            int        `db:"id" json:"id"`
	MessageID     *int       `db:"message_id" json:"message_id"`
	Filename      string     `db:"filename" json:"filename"`
	Path          string     `db:"path" json:"path"`
	Size          int64      `db:"size" json:"size"`
	MimeType      *string    `db:"mime_type" json:"mime_type"`
	ShareToken    *string    `db:"share_token" json:"share_token"`
	IsFolder      bool       `db:"is_folder" json:"is_folder"`
	ThumbPath     *string    `db:"thumb_path" json:"thumb_path"`
	Owner         string     `db:"owner" json:"owner"`
	CreatedAt     time.Time  `db:"created_at" json:"created_at"`
	DeletedAt     *time.Time `db:"deleted_at" json:"deleted_at,omitempty"`
	SharePassword *string    `db:"share_password" json:"-"`

	// Virtual fields
	DirectToken string `db:"-" json:"direct_token,omitempty"`
	HasThumb    bool   `db:"-" json:"has_thumb"`
}

type User struct {
	ID           int       `db:"id" json:"id"`
	Username     string    `db:"username" json:"username"`
	PasswordHash string    `db:"password_hash" json:"-"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
	FileCount    int       `json:"file_count"`
	TotalSize    int64     `json:"total_size"`
}

type WrappedDB struct {
	*sqlx.DB
}

func RebindQuery(query string) string {
	if driverName == "postgres" &&
		!strings.Contains(query, "$1") {
		return sqlx.Rebind(sqlx.DOLLAR, query)
	}

	return query
}

func (db *WrappedDB) Exec(query string, args ...interface{}) (sql.Result, error) {
	return db.DB.Exec(RebindQuery(query), args...)
}

func (db *WrappedDB) Get(dest interface{}, query string, args ...interface{}) error {
	return db.DB.Get(dest, RebindQuery(query), args...)
}

func (db *WrappedDB) Select(dest interface{}, query string, args ...interface{}) error {
	return db.DB.Select(dest, RebindQuery(query), args...)
}

func (db *WrappedDB) Query(query string, args ...interface{}) (*sql.Rows, error) {
	return db.DB.Query(RebindQuery(query), args...)
}

func (db *WrappedDB) QueryRow(query string, args ...interface{}) *sql.Row {
	return db.DB.QueryRow(RebindQuery(query), args...)
}

func (db *WrappedDB) NamedExec(query string, arg interface{}) (sql.Result, error) {
	return db.DB.NamedExec(RebindQuery(query), arg)
}

type WrappedTx struct {
	*sqlx.Tx
}

func (tx *WrappedTx) Exec(query string, args ...interface{}) (sql.Result, error) {
	return tx.Tx.Exec(RebindQuery(query), args...)
}

func (tx *WrappedTx) Get(dest interface{}, query string, args ...interface{}) error {
	return tx.Tx.Get(dest, RebindQuery(query), args...)
}

func (tx *WrappedTx) Select(dest interface{}, query string, args ...interface{}) error {
	return tx.Tx.Select(dest, RebindQuery(query), args...)
}

func (db *WrappedDB) Beginx() (*WrappedTx, error) {
	tx, err := db.DB.Beginx()
	if err != nil {
		return nil, err
	}

	return &WrappedTx{tx}, nil
}

var (
	DB   *WrappedDB // Alias to RWDB for backward compatibility
	RWDB *WrappedDB // Write pool (MaxOpenConns=1 for SQLite)
	RODB *WrappedDB // Read pool (MaxOpenConns=10 for SQLite)
)
var driverName = "sqlite"

func InitDB(driver, dbPath, dbDSN string) error {
	var err error
	var rawDB *sqlx.DB
	var rawRWDB *sqlx.DB
	var rawRODB *sqlx.DB

	driverName = strings.ToLower(strings.TrimSpace(driver))
	if driverName == "" {
		driverName = "sqlite"
	}

	var schema string
	switch driverName {
	case "sqlite":
		// Add PRAGMA settings to improve concurrency and prevent SQLITE_BUSY errors.
		dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", dbPath)

		// Initialize Write Pool (MaxOpenConns=1)
		rawRWDB, err = sqlx.Connect("sqlite", dsn)
		if err != nil {
			return fmt.Errorf("failed to connect to write database: %v", err)
		}
		rawRWDB.SetMaxOpenConns(1)

		RWDB = &WrappedDB{rawRWDB}

		// Initialize Read Pool (MaxOpenConns=10)
		rawRODB, err = sqlx.Connect("sqlite", dsn)
		if err != nil {
			return fmt.Errorf("failed to connect to read database: %v", err)
		}
		rawRODB.SetMaxOpenConns(10)

		RODB = &WrappedDB{rawRODB}

		DB = RWDB // Alias for existing code
		schema = sqliteSchema
	case "mysql":
		if dbDSN == "" {
			return fmt.Errorf("DATABASE_DSN must be set when DATABASE_DRIVER=mysql")
		}
		rawDB, err = sqlx.Connect("mysql", normalizeMySQLDSN(dbDSN))
		if err != nil {
			return err
		}
		DB = &WrappedDB{rawDB}
		RWDB = DB
		RODB = DB
		schema = mysqlSchema
	case "postgres":
		if dbDSN == "" {
			return fmt.Errorf("DATABASE_DSN must be set when DATABASE_DRIVER=postgres")
		}

		rawDB, err = sqlx.Connect("postgres", dbDSN)
		if err != nil {
			return err
		}
		DB = &WrappedDB{rawDB}
		RWDB = DB
		RODB = DB

		schema = postgresSchema
	default:
		return fmt.Errorf("unsupported DATABASE_DRIVER %q (supported: sqlite, mysql)", driver)
	}

	if err := execSchema(schema); err != nil {
		return fmt.Errorf("failed to create schema: %v", err)
	}

	if driverName == "mysql" {
		if err := migrateMySQL(); err != nil {
			return err
		}
	} else if driverName == "postgres" {
		if err := migratePostgres(); err != nil {
			return err
		}
	} else {
		if err := migrateSQLite(); err != nil {
			return err
		}
		// Ensure old unique index is gone before backfill (it might exist from previous versions)
		DB.Exec("DROP INDEX IF EXISTS idx_files_path_filename_owner")
		if err := backfillOwners(); err != nil {
			return err
		}
		// Deduplicate files to avoid "UNIQUE constraint failed" when creating the index
		DB.Exec("DELETE FROM files WHERE id NOT IN (SELECT MIN(id) FROM files GROUP BY path, filename, owner)")
		// Create unique index for active files only (SQLite 3.8.0+)
		if _, err := DB.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_active_files ON files (path, filename, owner) WHERE deleted_at IS NULL"); err != nil {
			return fmt.Errorf("failed to create unique index: %v", err)
		}
	}
	return nil
}

const sqliteSchema = `
	CREATE TABLE IF NOT EXISTS files (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		message_id INTEGER,
		filename TEXT NOT NULL,
		path TEXT DEFAULT '/',
		size INTEGER DEFAULT 0,
		mime_type TEXT,
		share_token TEXT UNIQUE,
		is_folder BOOLEAN DEFAULT 0,
		thumb_path TEXT,
		owner TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME,
		share_password TEXT
	);

	CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS sessions (
		token TEXT PRIMARY KEY,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		username TEXT DEFAULT ''
	);

	CREATE TABLE IF NOT EXISTS tg_sessions (
		session_id TEXT PRIMARY KEY,
		data BLOB NOT NULL,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS child_accounts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		api_key TEXT UNIQUE,
		webdav_enabled INTEGER DEFAULT 1,
		api_enabled INTEGER DEFAULT 1,
		force_password_change INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS passkeys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL,
		credential_id BLOB UNIQUE NOT NULL,
		public_key BLOB NOT NULL,
		attestation_type TEXT,
		aaguid BLOB,
		sign_count INTEGER DEFAULT 0,
		transports TEXT,
		name TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS file_parts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		file_id INTEGER NOT NULL,
		message_id INTEGER NOT NULL,
		part_index INTEGER NOT NULL,
		size INTEGER NOT NULL,
		FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS upload_tasks (
		id TEXT PRIMARY KEY,
		filename TEXT NOT NULL,
		owner TEXT NOT NULL,
		overwrite BOOLEAN DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS upload_chunks (
		task_id TEXT NOT NULL,
		chunk_index INTEGER NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (task_id, chunk_index)
	);

	CREATE TABLE IF NOT EXISTS user_settings (
		username TEXT NOT NULL,
		key TEXT NOT NULL,
		value TEXT NOT NULL,
		PRIMARY KEY (username, key)
	);

	CREATE INDEX IF NOT EXISTS idx_files_path ON files(path);
	CREATE INDEX IF NOT EXISTS idx_files_owner_path ON files(owner, path, filename);
	CREATE INDEX IF NOT EXISTS idx_files_message_id ON files(message_id);
	CREATE INDEX IF NOT EXISTS idx_passkeys_username ON passkeys(username);
	CREATE INDEX IF NOT EXISTS idx_file_parts_file_id ON file_parts(file_id);
	`

const mysqlSchema = `
	CREATE TABLE IF NOT EXISTS files (
		id BIGINT PRIMARY KEY AUTO_INCREMENT,
		message_id BIGINT,
		filename VARCHAR(191) NOT NULL,
		path VARCHAR(384) DEFAULT '/',
		size BIGINT DEFAULT 0,
		mime_type VARCHAR(255),
		share_token VARCHAR(191) UNIQUE,
		is_folder TINYINT(1) DEFAULT 0,
		thumb_path VARCHAR(1024),
		owner VARCHAR(191) DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME,
		share_password TEXT
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

	CREATE TABLE IF NOT EXISTS settings (
		` + "`key`" + ` VARCHAR(191) PRIMARY KEY,
		value TEXT NOT NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

	CREATE TABLE IF NOT EXISTS sessions (
		token VARCHAR(191) PRIMARY KEY,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		username VARCHAR(191) DEFAULT ''
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

	CREATE TABLE IF NOT EXISTS tg_sessions (
		session_id VARCHAR(191) PRIMARY KEY,
		data LONGBLOB NOT NULL,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

	CREATE TABLE IF NOT EXISTS child_accounts (
		id BIGINT PRIMARY KEY AUTO_INCREMENT,
		username VARCHAR(191) UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		api_key VARCHAR(191) UNIQUE,
		webdav_enabled TINYINT(1) DEFAULT 1,
		api_enabled TINYINT(1) DEFAULT 1,
		force_password_change TINYINT(1) DEFAULT 0,
		s3_access_key VARCHAR(191) UNIQUE,
		s3_secret_key TEXT,
		s3_enabled TINYINT(1) DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

	CREATE TABLE IF NOT EXISTS passkeys (
		id BIGINT PRIMARY KEY AUTO_INCREMENT,
		username VARCHAR(191) NOT NULL,
		credential_id VARBINARY(255) UNIQUE NOT NULL,
		public_key BLOB NOT NULL,
		attestation_type VARCHAR(255),
		aaguid VARBINARY(16),
		sign_count BIGINT DEFAULT 0,
		transports TEXT,
		backup_eligible TINYINT(1) DEFAULT 0,
		backup_state TINYINT(1) DEFAULT 0,
		name VARCHAR(255),
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

	CREATE TABLE IF NOT EXISTS file_parts (
		id BIGINT PRIMARY KEY AUTO_INCREMENT,
		file_id BIGINT NOT NULL,
		message_id BIGINT NOT NULL,
		part_index BIGINT NOT NULL,
		size BIGINT NOT NULL,
		FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

	CREATE TABLE IF NOT EXISTS upload_tasks (
		id VARCHAR(191) PRIMARY KEY,
		filename VARCHAR(191) NOT NULL,
		owner VARCHAR(191) NOT NULL,
		overwrite TINYINT(1) DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

	CREATE TABLE IF NOT EXISTS upload_chunks (
		task_id VARCHAR(191) NOT NULL,
		chunk_index BIGINT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (task_id, chunk_index)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

	CREATE TABLE IF NOT EXISTS user_settings (
		username VARCHAR(191) NOT NULL,
		` + "`key`" + ` VARCHAR(191) NOT NULL,
		value TEXT NOT NULL,
		PRIMARY KEY (username, ` + "`key`" + `)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const postgresSchema = `
	CREATE TABLE IF NOT EXISTS files (
		id BIGSERIAL PRIMARY KEY,
		message_id BIGINT,
		filename TEXT NOT NULL,
		path TEXT DEFAULT '/',
		size BIGINT DEFAULT 0,
		mime_type TEXT,
		share_token TEXT UNIQUE,
		is_folder SMALLINT DEFAULT 0,
		thumb_path TEXT,
		owner TEXT DEFAULT '',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		deleted_at TIMESTAMP,
		share_password TEXT
	);

	CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS sessions (
		token TEXT PRIMARY KEY,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		username TEXT DEFAULT ''
	);

	CREATE TABLE IF NOT EXISTS tg_sessions (
		session_id TEXT PRIMARY KEY,
		data BYTEA NOT NULL,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS child_accounts (
		id BIGSERIAL PRIMARY KEY,
		username TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		api_key TEXT UNIQUE,
		webdav_enabled SMALLINT DEFAULT 1,
		api_enabled SMALLINT DEFAULT 1,
		force_password_change SMALLINT DEFAULT 0,
		s3_access_key TEXT UNIQUE,
		s3_secret_key TEXT,
		s3_enabled SMALLINT DEFAULT 1,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS passkeys (
		id BIGSERIAL PRIMARY KEY,
		username TEXT NOT NULL,
		credential_id BYTEA UNIQUE NOT NULL,
		public_key BYTEA NOT NULL,
		attestation_type TEXT,
		aaguid BYTEA,
		sign_count BIGINT DEFAULT 0,
		transports TEXT,
		backup_eligible SMALLINT DEFAULT 0,
		backup_state SMALLINT DEFAULT 0,
		name TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS file_parts (
		id BIGSERIAL PRIMARY KEY,
		file_id BIGINT NOT NULL REFERENCES files(id) ON DELETE CASCADE,
		message_id BIGINT NOT NULL,
		part_index BIGINT NOT NULL,
		size BIGINT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS upload_tasks (
		id TEXT PRIMARY KEY,
		filename TEXT NOT NULL,
		owner TEXT NOT NULL,
		overwrite SMALLINT DEFAULT 0,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS upload_chunks (
		task_id TEXT NOT NULL,
		chunk_index BIGINT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (task_id, chunk_index)
	);

	CREATE TABLE IF NOT EXISTS user_settings (
		username TEXT NOT NULL,
		key TEXT NOT NULL,
		value TEXT NOT NULL,
		PRIMARY KEY (username, key)
	);

	CREATE INDEX IF NOT EXISTS idx_files_path ON files(path);
	CREATE INDEX IF NOT EXISTS idx_files_owner_path ON files(owner, path, filename);
	CREATE INDEX IF NOT EXISTS idx_files_message_id ON files(message_id);
	CREATE INDEX IF NOT EXISTS idx_passkeys_username ON passkeys(username);
	CREATE INDEX IF NOT EXISTS idx_file_parts_file_id ON file_parts(file_id);
`

func migrateSQLite() error {
	// Migration for existing DBs
	DB.Exec("ALTER TABLE sessions ADD COLUMN username TEXT DEFAULT ''")
	DB.Exec("ALTER TABLE child_accounts ADD COLUMN api_key TEXT")
	DB.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_child_accounts_api_key ON child_accounts(api_key)")
	DB.Exec("ALTER TABLE child_accounts ADD COLUMN webdav_enabled INTEGER DEFAULT 1")
	DB.Exec("ALTER TABLE child_accounts ADD COLUMN api_enabled INTEGER DEFAULT 1")
	DB.Exec("ALTER TABLE child_accounts ADD COLUMN force_password_change INTEGER DEFAULT 0")
	DB.Exec("ALTER TABLE passkeys ADD COLUMN backup_eligible BOOLEAN DEFAULT 0")
	DB.Exec("ALTER TABLE passkeys ADD COLUMN backup_state BOOLEAN DEFAULT 0")
	DB.Exec("ALTER TABLE passkeys ADD COLUMN name TEXT")
	DB.Exec("ALTER TABLE files ADD COLUMN owner TEXT DEFAULT ''")
	DB.Exec("CREATE TABLE IF NOT EXISTS user_settings (username TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY (username, key))")

	DB.Exec("ALTER TABLE child_accounts ADD COLUMN s3_access_key TEXT")
	DB.Exec("ALTER TABLE child_accounts ADD COLUMN s3_secret_key TEXT")
	DB.Exec("ALTER TABLE child_accounts ADD COLUMN s3_enabled INTEGER DEFAULT 1")
	DB.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_child_accounts_s3_key ON child_accounts(s3_access_key)")

	DB.Exec("CREATE INDEX IF NOT EXISTS idx_files_owner_path ON files(owner, path, filename)")
	DB.Exec("CREATE TABLE IF NOT EXISTS tg_sessions (session_id TEXT PRIMARY KEY, data BLOB NOT NULL, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)")
	DB.Exec("ALTER TABLE upload_tasks ADD COLUMN overwrite BOOLEAN DEFAULT 0")
	DB.Exec("ALTER TABLE files ADD COLUMN deleted_at DATETIME")
	DB.Exec("ALTER TABLE files ADD COLUMN share_password TEXT")
	// Ensure foreign keys are enabled
	DB.Exec("PRAGMA foreign_keys = ON")
	return nil
}

func execSchema(schema string) error {
	for _, statement := range strings.Split(schema, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := DB.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func migrateMySQL() error {
	if err := alterTableMySQL("sessions", "ADD COLUMN username VARCHAR(191) DEFAULT ''"); err != nil {
		return err
	}
	if err := alterTableMySQL("child_accounts", "ADD COLUMN api_key VARCHAR(191)"); err != nil {
		return err
	}
	if err := createIndexMySQL("idx_child_accounts_api_key", "child_accounts", "api_key", true); err != nil {
		return err
	}
	if err := alterTableMySQL("child_accounts", "ADD COLUMN webdav_enabled TINYINT(1) DEFAULT 1"); err != nil {
		return err
	}
	if err := alterTableMySQL("child_accounts", "ADD COLUMN api_enabled TINYINT(1) DEFAULT 1"); err != nil {
		return err
	}
	if err := alterTableMySQL("child_accounts", "ADD COLUMN force_password_change TINYINT(1) DEFAULT 0"); err != nil {
		return err
	}
	if err := alterTableMySQL("passkeys", "ADD COLUMN backup_eligible TINYINT(1) DEFAULT 0"); err != nil {
		return err
	}
	if err := alterTableMySQL("passkeys", "ADD COLUMN backup_state TINYINT(1) DEFAULT 0"); err != nil {
		return err
	}
	if err := alterTableMySQL("passkeys", "ADD COLUMN name VARCHAR(255)"); err != nil {
		return err
	}
	if err := alterTableMySQL("files", "ADD COLUMN owner VARCHAR(191) DEFAULT ''"); err != nil {
		return err
	}

	// Drop index if exists to avoid collisions during backfill (handling potential dirty state)
	if IsMySQL() {
		DB.Exec("DROP INDEX idx_files_path_filename_owner ON files")
	}

	// Backfill owners BEFORE creating the unique index to avoid collisions with empty values
	if err := backfillOwners(); err != nil {
		return err
	}

	if err := createIndexMySQL("idx_files_path", "files", "path", false); err != nil {
		return err
	}
	if err := createIndexMySQL("idx_files_owner_path", "files", "owner, path, filename", false); err != nil {
		return err
	}
	if err := createIndexMySQL("idx_files_message_id", "files", "message_id", false); err != nil {
		return err
	}
	if err := createIndexMySQL("idx_passkeys_username", "passkeys", "username", false); err != nil {
		return err
	}
	if err := createIndexMySQL("idx_file_parts_file_id", "file_parts", "file_id", false); err != nil {
		return err
	}
	// Deduplicate files for MySQL
	DB.Exec("DELETE FROM files WHERE id NOT IN (SELECT * FROM (SELECT MIN(id) FROM files GROUP BY path, filename, owner) AS t)")
	if err := createIndexMySQL("idx_files_path_filename_owner", "files", "path, filename, owner", false); err != nil {
		return err
	}
	if err := alterTableMySQL("child_accounts", "ADD COLUMN s3_access_key VARCHAR(191)"); err != nil {
		return err
	}
	if err := alterTableMySQL("child_accounts", "ADD COLUMN s3_secret_key TEXT"); err != nil {
		return err
	}
	if err := alterTableMySQL("child_accounts", "ADD COLUMN s3_enabled TINYINT(1) DEFAULT 1"); err != nil {
		return err
	}
	if err := createIndexMySQL("idx_child_accounts_s3_key", "child_accounts", "s3_access_key", true); err != nil {
		return err
	}

	if _, err := DB.Exec("CREATE TABLE IF NOT EXISTS tg_sessions (session_id VARCHAR(191) PRIMARY KEY, data LONGBLOB NOT NULL, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci"); err != nil {
		return err
	}

	if err := alterTableMySQL("upload_tasks", "ADD COLUMN overwrite TINYINT(1) DEFAULT 0"); err != nil {
		return err
	}
	if err := alterTableMySQL("files", "ADD COLUMN deleted_at DATETIME"); err != nil {
		return err
	}
	if err := alterTableMySQL("files", "ADD COLUMN share_password TEXT"); err != nil {
		return err
	}

	// Create user_settings if not exists (already in schema but for migration)
	if _, err := DB.Exec("CREATE TABLE IF NOT EXISTS user_settings (username VARCHAR(191) NOT NULL, `key` VARCHAR(191) NOT NULL, value TEXT NOT NULL, PRIMARY KEY (username, `key`)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci"); err != nil {
		return err
	}

	return nil
}

func migratePostgres() error {
	_, err := DB.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_active_files
		ON files (path, filename, owner)
		WHERE deleted_at IS NULL
	`)
	return err
}

func normalizeMySQLDSN(dsn string) string {
	if strings.Contains(dsn, "?") {
		if !strings.Contains(dsn, "parseTime=") {
			dsn += "&parseTime=true"
		}
		if !strings.Contains(dsn, "charset=") {
			dsn += "&charset=utf8mb4"
		}
		return dsn
	}
	return dsn + "?parseTime=true&charset=utf8mb4"
}

func createIndexMySQL(name, table, columns string, unique bool) error {
	var count int
	err := DB.Get(&count, "SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?", table, name)
	if err != nil || count > 0 {
		return err
	}
	uniqueSQL := ""
	if unique {
		uniqueSQL = "UNIQUE "
	}
	_, err = DB.Exec(fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)", uniqueSQL, name, table, columns))
	return err
}

func columnExistsMySQL(table, column string) bool {
	var count int
	err := DB.Get(&count, "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?", table, column)
	return err == nil && count > 0
}

func alterTableMySQL(table, action string) error {
	// Simple heuristic to extract column name from "ADD COLUMN name TYPE"
	parts := strings.Fields(action)
	if len(parts) >= 3 && strings.ToUpper(parts[0]) == "ADD" && strings.ToUpper(parts[1]) == "COLUMN" {
		col := parts[2]
		if columnExistsMySQL(table, col) {
			return nil
		}
	}
	_, err := DB.Exec(fmt.Sprintf("ALTER TABLE %s %s", table, action))
	return err
}

func backfillOwners() error {
	var usernames []string
	err := DB.Select(&usernames, "SELECT username FROM child_accounts")
	if err != nil {
		// Table might not exist yet if fresh install, ignore
		return nil
	}

	updateCmd := "UPDATE"
	if IsMySQL() {
		updateCmd = "UPDATE IGNORE"
	} else {
		updateCmd = "UPDATE OR IGNORE"
	}

	for _, u := range usernames {
		prefix := "/" + u
		if _, err := DB.Exec(fmt.Sprintf("%s files SET owner = ? WHERE (path = ? OR path LIKE ?) AND (owner IS NULL OR owner = '')", updateCmd), u, prefix, prefix+"/%"); err != nil {
			return fmt.Errorf("failed to backfill owner for user %s path: %v", u, err)
		}
		if _, err := DB.Exec(fmt.Sprintf("%s files SET owner = ? WHERE path = '/' AND filename = ? AND is_folder = 1 AND (owner IS NULL OR owner = '')", updateCmd), u, u); err != nil {
			return fmt.Errorf("failed to backfill owner for user %s root folder: %v", u, err)
		}
	}

	// Set remaining empty owners to Admin
	adminUser := GetSetting("admin_username")
	if adminUser != "" {
		if _, err := DB.Exec(fmt.Sprintf("%s files SET owner = ? WHERE (owner IS NULL OR owner = '')", updateCmd), adminUser); err != nil {
			return fmt.Errorf("failed to backfill owner for admin: %v", err)
		}
	}

	return nil
}

func IsMySQL() bool {
	return driverName == "mysql"
}

func IsPostgres() bool {
	return driverName == "postgres"
}

func InsertIgnoreSQL(table, columns, values string) string {
	if IsMySQL() {
		return fmt.Sprintf("INSERT IGNORE INTO %s (%s) VALUES (%s)", table, columns, values)
	}
	if IsPostgres() {
		return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT DO NOTHING", table, columns, values)
	}
	return fmt.Sprintf("INSERT OR IGNORE INTO %s (%s) VALUES (%s)", table, columns, values)
}

func ConcatPathSQL() string {
	if IsMySQL() {
		return "CONCAT(?, SUBSTR(path, ?))"
	}
	return "? || SUBSTR(path, ?)"
}

type FilePart struct {
	ID        int   `db:"id" json:"id"`
	FileID    int   `db:"file_id" json:"file_id"`
	MessageID int   `db:"message_id" json:"message_id"`
	PartIndex int   `db:"part_index" json:"part_index"`
	Size      int64 `db:"size" json:"size"`
}

func GetFileParts(fileID int) ([]FilePart, error) {
	var parts []FilePart
	err := RODB.Select(&parts, "SELECT * FROM file_parts WHERE file_id = ? ORDER BY part_index ASC", fileID)
	return parts, err
}

func GetSetting(key string) string {
	var value string
	query := "SELECT value FROM settings WHERE `key` = ?"
	if !IsMySQL() {
		query = "SELECT value FROM settings WHERE key = ?"
	}
	err := RODB.Get(&value, query, key)
	if err != nil {
		return ""
	}
	return value
}

func SetSetting(key string, value string) error {
	query := "INSERT INTO settings (`key`, value) VALUES (?, ?) ON DUPLICATE KEY UPDATE value = VALUES(value)"
	if !IsMySQL() {
		query = "INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value"
	}
	_, err := DB.Exec(query, key, value)
	return err
}

func GetTGSession(sessionID string) ([]byte, error) {
	var data []byte
	err := RODB.Get(&data, "SELECT data FROM tg_sessions WHERE session_id = ?", sessionID)
	return data, err
}

func SetTGSession(sessionID string, data []byte) error {
	query := "INSERT INTO tg_sessions (session_id, data, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP) ON DUPLICATE KEY UPDATE data = VALUES(data), updated_at = VALUES(updated_at)"
	if !IsMySQL() {
		query = "INSERT INTO tg_sessions (session_id, data, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP) ON CONFLICT(session_id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at"
	}
	_, err := DB.Exec(query, sessionID, data)
	return err
}

func DeleteSetting(key string) error {
	query := "DELETE FROM settings WHERE `key` = ?"
	if !IsMySQL() {
		query = "DELETE FROM settings WHERE key = ?"
	}
	_, err := DB.Exec(query, key)
	return err
}

func GetUserSetting(username string, key string) string {
	var value string
	query := "SELECT value FROM user_settings WHERE username = ? AND `key` = ?"
	if !IsMySQL() {
		query = "SELECT value FROM user_settings WHERE username = ? AND key = ?"
	}
	err := RODB.Get(&value, query, username, key)
	if err != nil {
		return ""
	}
	return value
}

func SetUserSetting(username string, key string, value string) error {
	query := "INSERT INTO user_settings (username, `key`, value) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE value = VALUES(value)"
	if !IsMySQL() {
		query = "INSERT INTO user_settings (username, key, value) VALUES (?, ?, ?) ON CONFLICT(username, key) DO UPDATE SET value = excluded.value"
	}
	_, err := DB.Exec(query, username, key, value)
	return err
}

type Queryer interface {
	Get(dest interface{}, query string, args ...interface{}) error
}

func GetUniqueFilename(q Queryer, path, filename string, isFolder bool, excludeID int, owner string) string {
	if filename == "" {
		return "unnamed"
	}

	finalName := filename
	counter := 1

	for counter <= 1000 {
		var id int
		err := q.Get(&id, "SELECT id FROM files WHERE path = ? AND filename = ? AND owner = ? AND id != ? AND deleted_at IS NULL LIMIT 1", path, finalName, owner, excludeID)
		if err != nil { // Not found or error
			break
		}

		if isFolder {
			finalName = fmt.Sprintf("%s (%d)", filename, counter)
		} else {
			dotIndex := -1
			for i := len(filename) - 1; i >= 0; i-- {
				if filename[i] == '.' {
					dotIndex = i
					break
				}
			}
			if dotIndex == -1 {
				finalName = fmt.Sprintf("%s (%d)", filename, counter)
			} else {
				name := filename[:dotIndex]
				ext := filename[dotIndex:]
				finalName = fmt.Sprintf("%s (%d)%s", name, counter, ext)
			}
		}
		counter++
	}
	return finalName
}

func EnsureFoldersExist(dbPath string, owner string) error {
	cleanPath := path.Clean(dbPath)
	if cleanPath == "/" {
		return nil
	}

	parts := strings.Split(cleanPath, "/")
	currentPath := "/"

	for i := 1; i < len(parts); i++ {
		part := parts[i]
		if part == "" {
			continue
		}

		var id int
		err := RODB.Get(&id, "SELECT id FROM files WHERE path = ? AND filename = ? AND is_folder = 1 AND owner = ?", currentPath, part, owner)
		if err != nil {
			var count int
			if currentPath == "/" {
				RODB.Get(&count, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", part)
			}

			if count == 0 {
				_, err = DB.Exec(InsertIgnoreSQL("files", "filename, path, is_folder, owner", "?, ?, 1, ?"), part, currentPath, owner)
				if err != nil {
					return err
				}
			}
		}

		if currentPath == "/" {
			currentPath = "/" + part
		} else {
			currentPath = currentPath + "/" + part
		}
	}
	return nil
}

func EnsureFoldersExistTx(tx *WrappedTx, dbPath string, owner string) error {
	cleanPath := path.Clean(dbPath)
	if cleanPath == "/" {
		return nil
	}

	parts := strings.Split(cleanPath, "/")
	currentPath := "/"

	for i := 1; i < len(parts); i++ {
		part := parts[i]
		if part == "" {
			continue
		}

		var id int
		err := tx.Get(&id, "SELECT id FROM files WHERE path = ? AND filename = ? AND is_folder = 1 AND owner = ? AND deleted_at IS NULL", currentPath, part, owner)
		if err != nil {
			var count int
			if currentPath == "/" {
				tx.Get(&count, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", part)
			}

			if count == 0 {
				_, err = tx.Exec(InsertIgnoreSQL("files", "filename, path, is_folder, owner", "?, ?, 1, ?"), part, currentPath, owner)
				if err != nil {
					return err
				}
			}
		}

		if currentPath == "/" {
			currentPath = "/" + part
		} else {
			currentPath = currentPath + "/" + part
		}
	}
	return nil
}

func CloseDB() error {
	var errs []string
	if RWDB != nil {
		if err := RWDB.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("RWDB: %v", err))
		}
	}
	if RODB != nil && RODB != RWDB {
		if err := RODB.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("RODB: %v", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to close database: %s", strings.Join(errs, ", "))
	}
	return nil
}

// GetOrphanedMessages identifies Telegram Message IDs that are only used by the provided file IDs.
// It ensures that if other files still reference a message (e.g. via copy), that message is not deleted.
func GetOrphanedMessages(fileIDs []int) ([]int, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}

	// Create placeholders for the file IDs
	placeholders := make([]string, len(fileIDs))
	args := make([]interface{}, len(fileIDs))
	for i, id := range fileIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	idList := strings.Join(placeholders, ",")

	// We want message_ids that exist in the set of files being deleted,
	// BUT do NOT exist in the set of files NOT being deleted.

	query := fmt.Sprintf(`
		SELECT DISTINCT message_id FROM (
			SELECT message_id FROM files WHERE id IN (%s) AND message_id IS NOT NULL
			UNION
			SELECT message_id FROM file_parts WHERE file_id IN (%s)
		) AS to_delete
		WHERE message_id NOT IN (
			SELECT message_id FROM files WHERE id NOT IN (%s) AND message_id IS NOT NULL
			UNION
			SELECT message_id FROM file_parts WHERE file_id NOT IN (%s)
		)`, idList, idList, idList, idList)

	// Combine args: we need the same fileIDs list 4 times
	fullArgs := make([]interface{}, 0, len(fileIDs)*4)
	for i := 0; i < 4; i++ {
		fullArgs = append(fullArgs, args...)
	}

	var orphanedIDs []int
	err := RODB.Select(&orphanedIDs, query, fullArgs...)
	return orphanedIDs, err
}
