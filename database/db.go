package database

import (
	"fmt"
	"log"
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
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
	
	// Virtual fields
	DirectToken string `db:"-" json:"direct_token,omitempty"`
	HasThumb    bool   `db:"-" json:"has_thumb"`
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
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS sessions (
		token TEXT PRIMARY KEY,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_files_path ON files(path);
	CREATE INDEX IF NOT EXISTS idx_files_message_id ON files(message_id);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_files_path_filename ON files(path, filename);
	`
	_, err = DB.Exec(schema)
	if err != nil {
		log.Fatalf("Failed to create schema: %v", err)
	}
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

type Queryer interface {
	Get(dest interface{}, query string, args ...interface{}) error
}

func GetUniqueFilename(q Queryer, path, filename string, isFolder bool, excludeID int) string {
	if filename == "" {
		return "unnamed"
	}

	finalName := filename
	counter := 1

	for counter <= 1000 {
		var id int
		err := q.Get(&id, "SELECT id FROM files WHERE path = ? AND filename = ? AND id != ? LIMIT 1", path, finalName, excludeID)
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
