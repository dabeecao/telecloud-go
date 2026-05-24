package tgclient

import (
	"archive/zip"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"telecloud/config"
	"telecloud/database"
)

// PerformRestore restores database and configurations from an uploaded ZIP or DB file.
func PerformRestore(cfg *config.Config, uploadedPath string) error {
	tempDir := filepath.Join(cfg.TempDir, "restore_temp")
	os.MkdirAll(tempDir, 0755)
	defer os.RemoveAll(tempDir)

	tempDBPath := filepath.Join(tempDir, "database.db")
	tempKeyPath := filepath.Join(tempDir, "master.key")

	isZip := false
	// Detect if zip file
	f, err := os.Open(uploadedPath)
	if err != nil {
		return fmt.Errorf("failed to open uploaded file: %w", err)
	}

	// Read first 4 bytes to check ZIP signature (PK\x03\x04)
	header := make([]byte, 4)
	n, _ := f.Read(header)
	f.Close()
	if n == 4 && string(header) == "PK\x03\x04" {
		isZip = true
	}

	if !isZip {
		// Treat directly as a .db file. This is only supported for SQLite databases.
		if !database.IsSQLite() {
			return fmt.Errorf("restoring raw DB file (.db, .sqlite) is not supported when using MySQL/Postgres. Please upload a ZIP backup")
		}

		srcFile, err := os.Open(uploadedPath)
		if err != nil {
			return fmt.Errorf("failed to open db file: %w", err)
		}
		defer srcFile.Close()

		dstFile, err := os.Create(tempDBPath)
		if err != nil {
			return fmt.Errorf("failed to create temp db file: %w", err)
		}
		defer dstFile.Close()

		_, err = io.Copy(dstFile, srcFile)
		if err != nil {
			return fmt.Errorf("failed to copy database file: %w", err)
		}
	} else {
		// Open ZIP archive
		archive, err := zip.OpenReader(uploadedPath)
		if err != nil {
			return fmt.Errorf("failed to open zip archive: %w", err)
		}
		defer archive.Close()

		dbFound := false
		keyFound := false
		thumbsFound := false

		for _, file := range archive.File {
			// 1. Extract database.db (only used/required if IsSQLite)
			if file.Name == "database.db" || strings.HasSuffix(file.Name, "/database.db") {
				if database.IsSQLite() {
					dstFile, err := os.Create(tempDBPath)
					if err != nil {
						return fmt.Errorf("failed to create temp db file: %w", err)
					}
					srcFile, err := file.Open()
					if err != nil {
						dstFile.Close()
						return fmt.Errorf("failed to open file in zip: %w", err)
					}
					_, err = io.Copy(dstFile, srcFile)
					srcFile.Close()
					dstFile.Close()
					if err != nil {
						return fmt.Errorf("failed to extract database.db: %w", err)
					}
					dbFound = true
				}
			}

			// 2. Extract master.key
			if file.Name == "master.key" || strings.HasSuffix(file.Name, "/master.key") {
				dstFile, err := os.Create(tempKeyPath)
				if err != nil {
					return fmt.Errorf("failed to create temp key file: %w", err)
				}
				srcFile, err := file.Open()
				if err != nil {
					dstFile.Close()
					return fmt.Errorf("failed to open file in zip: %w", err)
				}
				_, err = io.Copy(dstFile, srcFile)
				srcFile.Close()
				dstFile.Close()
				if err != nil {
					return fmt.Errorf("failed to extract master.key: %w", err)
				}
				keyFound = true
			}

			// 3. Extract thumbnails
			if strings.HasPrefix(file.Name, "thumbnails/") {
				relPath := strings.TrimPrefix(file.Name, "thumbnails/")
				if relPath != "" {
					destPath := filepath.Join(cfg.ThumbsDir, relPath)
					// Ensure parent directory exists
					os.MkdirAll(filepath.Dir(destPath), 0755)

					dstFile, err := os.Create(destPath)
					if err != nil {
						return fmt.Errorf("failed to create thumbnail file: %w", err)
					}
					srcFile, err := file.Open()
					if err != nil {
						dstFile.Close()
						return fmt.Errorf("failed to open thumbnail file in zip: %w", err)
					}
					_, err = io.Copy(dstFile, srcFile)
					srcFile.Close()
					dstFile.Close()
					if err != nil {
						return fmt.Errorf("failed to extract thumbnail file: %w", err)
					}
					thumbsFound = true
				}
			}
		}

		if database.IsSQLite() && !dbFound {
			return fmt.Errorf("invalid backup: 'database.db' not found in ZIP bundle")
		}

		if !database.IsSQLite() && !keyFound && !thumbsFound {
			return fmt.Errorf("invalid backup: no master.key or thumbnails found in ZIP bundle")
		}
	}

	// For SQLite, perform database replacement
	if database.IsSQLite() {
		// Verify database integrity
		db, err := sql.Open("sqlite", tempDBPath)
		if err != nil {
			return fmt.Errorf("failed to open sqlite connection for validation: %w", err)
		}

		// Check if settings table exists
		var count int
		err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='settings'").Scan(&count)
		db.Close() // close connection before moving/deleting files
		if err != nil {
			return fmt.Errorf("database validation failed: %w", err)
		}
		if count == 0 {
			return fmt.Errorf("invalid backup: 'settings' table not found. Please upload a valid TeleCloud backup")
		}

		// Safe to restore! Close active DB
		err = database.CloseDB()
		if err != nil {
			return fmt.Errorf("failed to close active database connections: %w", err)
		}

		// Remove old wal and shm files to avoid SQLite WAL corruption/recovery failure
		os.Remove(cfg.DatabasePath + "-wal")
		os.Remove(cfg.DatabasePath + "-shm")

		// Overwrite active database file
		err = copyFile(tempDBPath, cfg.DatabasePath)
		if err != nil {
			return fmt.Errorf("failed to replace database.db: %w", err)
		}
	}

	// Overwrite master.key if a new one was found in the ZIP
	if _, err := os.Stat(tempKeyPath); err == nil {
		keyFile := resolveKeyFilePath()
		if keyFile != "" {
			// Overwrite existing master key file
			os.MkdirAll(filepath.Dir(keyFile), 0755)
			err = copyFile(tempKeyPath, keyFile)
			if err != nil {
				return fmt.Errorf("failed to replace master.key: %w", err)
			}
		}
	}

	return nil
}

func resolveKeyFilePath() string {
	if _, err := os.Stat("/app/data"); err == nil {
		return "/app/data/master.key"
	}
	if _, err := os.Stat("data"); err == nil {
		return "data/master.key"
	}
	dbPath := strings.TrimSpace(os.Getenv("DATABASE_PATH"))
	if dbPath != "" {
		return filepath.Join(filepath.Dir(dbPath), "master.key")
	}
	return "data/master.key"
}
