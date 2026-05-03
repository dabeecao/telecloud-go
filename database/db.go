package database

import (
	"fmt"
	"log"
	"path"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

type File struct {
	ID         int       `db:"id" json:"id"`
	MessageID  *int      `db:"message_id" json:"message_id"`
	Filename   string    `db:"filename" json:"filename"`
	Path       string    `db:"path" json:"path"`
	Size       int64     `db:"size" json:"size"`
	MimeType   *string   `db:"mime_type" json:"mime_type"`
	ShareToken *string   `db:"share_token" json:"share_token"`
	IsFolder   bool      `db:"is_folder" json:"is_folder"`
	ThumbPath  *string   `db:"thumb_path" json:"thumb_path"`
	Owner      string    `db:"owner" json:"owner"`
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
	
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

var DB *sqlx.DB

func InitDB(dbPath string) {
	var err error
	// Add PRAGMA settings to improve concurrency and prevent SQLITE_BUSY errors
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", dbPath)
	DB, err = sqlx.Connect("sqlite", dsn)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	// SQLite requires writes to be serialized
	DB.SetMaxOpenConns(1)

	schema := `
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
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
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
	CREATE INDEX IF NOT EXISTS idx_files_message_id ON files(message_id);
	CREATE INDEX IF NOT EXISTS idx_passkeys_username ON passkeys(username);
	CREATE INDEX IF NOT EXISTS idx_file_parts_file_id ON file_parts(file_id);
	`
	_, err = DB.Exec(schema)
	if err != nil {
		log.Fatalf("Failed to create schema: %v", err)
	}

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
	DB.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_files_path_filename_owner ON files(path, filename, owner)")
	DB.Exec("CREATE TABLE IF NOT EXISTS user_settings (username TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY (username, key))")
	
	// Ensure foreign keys are enabled
	DB.Exec("PRAGMA foreign_keys = ON")
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
	err := DB.Select(&parts, "SELECT * FROM file_parts WHERE file_id = ? ORDER BY part_index ASC", fileID)
	return parts, err
}

func GetSetting(key string) string {
	var value string
	err := DB.Get(&value, "SELECT value FROM settings WHERE key = ?", key)
	if err != nil {
		return ""
	}
	return value
}

func SetSetting(key string, value string) error {
	_, err := DB.Exec("INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

func DeleteSetting(key string) error {
	_, err := DB.Exec("DELETE FROM settings WHERE key = ?", key)
	return err
}

func GetUserSetting(username string, key string) string {
	var value string
	err := DB.Get(&value, "SELECT value FROM user_settings WHERE username = ? AND key = ?", username, key)
	if err != nil {
		return ""
	}
	return value
}

func SetUserSetting(username string, key string, value string) error {
	_, err := DB.Exec("INSERT INTO user_settings (username, key, value) VALUES (?, ?, ?) ON CONFLICT(username, key) DO UPDATE SET value = excluded.value", username, key, value)
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
		err := q.Get(&id, "SELECT id FROM files WHERE path = ? AND filename = ? AND owner = ? AND id != ? LIMIT 1", path, finalName, owner, excludeID)
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
		err := DB.Get(&id, "SELECT id FROM files WHERE path = ? AND filename = ? AND is_folder = 1", currentPath, part)
		if err != nil {
			var count int
			if currentPath == "/" {
				DB.Get(&count, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", part)
			}
			
			if count == 0 {
				_, err = DB.Exec("INSERT OR IGNORE INTO files (filename, path, is_folder, owner) VALUES (?, ?, 1, ?)", part, currentPath, owner)
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
