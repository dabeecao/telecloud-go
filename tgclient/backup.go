package tgclient

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"telecloud/config"
	"telecloud/database"
	"time"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/uploader"
)

var (
	backupMutex      sync.Mutex
	lastBackupTime   time.Time
	nextBackupTime   time.Time
	lastBackupStatus string
	isBackupRunning  bool
)

type BackupInfo struct {
	LastTime   string `json:"last_time"`
	NextTime   string `json:"next_time"`
	Status     string `json:"status"`
	IsRunning  bool   `json:"is_running"`
	SqliteOnly bool   `json:"sqlite_only"`
	Enabled    bool   `json:"enabled"`
}

func GetBackupInfo() BackupInfo {
	backupMutex.Lock()
	defer backupMutex.Unlock()
	
	lastTimeStr := ""
	if !lastBackupTime.IsZero() {
		lastTimeStr = lastBackupTime.Format(time.RFC3339)
	}

	nextTimeStr := ""
	if !nextBackupTime.IsZero() {
		nextTimeStr = nextBackupTime.Format(time.RFC3339)
	}
	
	return BackupInfo{
		LastTime:   lastTimeStr,
		NextTime:   nextTimeStr,
		Status:     lastBackupStatus,
		IsRunning:  isBackupRunning,
		SqliteOnly: database.IsSQLite(),
		Enabled:    database.GetSetting("backup_enabled") == "true",
	}
}

func PerformBackup(ctx context.Context, cfg *config.Config) error {
	backupMutex.Lock()
	if isBackupRunning {
		backupMutex.Unlock()
		return fmt.Errorf("backup is already running")
	}
	isBackupRunning = true
	backupMutex.Unlock()

	defer func() {
		backupMutex.Lock()
		isBackupRunning = false
		backupMutex.Unlock()
	}()

	log.Println("[Backup] Starting automated backup...")
	
	tempDir := filepath.Join(cfg.TempDir, "backups")
	os.MkdirAll(tempDir, 0755)
	defer os.RemoveAll(tempDir)

	timestamp := time.Now().Format("20060102_150405")
	var backupFiles []string

	// 1. Database Backup (SQLite only)
	if database.IsSQLite() {
		dbBackupPath := filepath.Join(tempDir, fmt.Sprintf("database_%s.db", timestamp))
		// Use VACUUM INTO for a safe, consistent copy of the SQLite database
		_, err := database.DB.Exec(fmt.Sprintf("VACUUM INTO '%s'", dbBackupPath))
		if err != nil {
			// Fallback to simple copy if VACUUM INTO is not supported (pre-3.27.0)
			log.Printf("[Backup] VACUUM INTO failed, falling back to copy: %v", err)
			err = copyFile(cfg.DatabasePath, dbBackupPath)
		}
		
		if err == nil {
			backupFiles = append(backupFiles, dbBackupPath)
		} else {
			log.Printf("[Backup] Failed to backup database: %v", err)
		}
	}

	// 2. Thumbnails Backup
	thumbsZipPath := filepath.Join(tempDir, fmt.Sprintf("thumbnails_%s.zip", timestamp))
	if err := zipDirectory(cfg.ThumbsDir, thumbsZipPath); err == nil {
		backupFiles = append(backupFiles, thumbsZipPath)
	} else {
		log.Printf("[Backup] Failed to backup thumbnails: %v", err)
	}

	if len(backupFiles) == 0 {
		backupMutex.Lock()
		lastBackupStatus = "failed: no files to backup"
		backupMutex.Unlock()
		return fmt.Errorf("no files to backup")
	}

	// 3. Upload to Telegram Log Group
	mainApi := GetAPI()
	peer, err := resolveLogGroup(ctx, mainApi, cfg.LogGroupID)
	if err != nil {
		backupMutex.Lock()
		lastBackupStatus = "failed: could not resolve log group"
		backupMutex.Unlock()
		return err
	}

	sender := message.NewSender(mainApi)
	up := uploader.NewUploader(mainApi).WithThreads(cfg.UploadThreads)

	for _, file := range backupFiles {
		log.Printf("[Backup] Uploading %s...", filepath.Base(file))
		
		f, err := os.Open(file)
		if err != nil {
			continue
		}
		
		stat, _ := f.Stat()
		if stat.Size() > cfg.MaxPartSize {
			f.Close()
			log.Printf("[Backup] File %s is too large (%d bytes), limit is %d bytes", file, stat.Size(), cfg.MaxPartSize)
			backupMutex.Lock()
			lastBackupStatus = "failed: file_too_large"
			backupMutex.Unlock()
			return fmt.Errorf("file too large")
		}

		tgFile, err := up.FromReader(ctx, filepath.Base(file), io.LimitReader(f, stat.Size()))
		f.Close()
		
		if err != nil {
			log.Printf("[Backup] Failed to upload %s: %v", file, err)
			continue
		}

		caption := fmt.Sprintf("<b>📦 TeleCloud Automated Backup</b>\n\n<b>File:</b> %s\n<b>Date:</b> %s\n\n#backup", filepath.Base(file), time.Now().Format("2006-01-02 15:04:05"))
		docBuilder := message.UploadedDocument(tgFile, html.String(nil, caption)).Filename(filepath.Base(file))
		_, err = sender.To(peer).Media(ctx, docBuilder)
		if err != nil {
			log.Printf("[Backup] Failed to send file to group: %v", err)
		}
	}

	backupMutex.Lock()
	lastBackupTime = time.Now()
	lastBackupStatus = "success"
	database.SetSetting("last_backup_time", lastBackupTime.Format(time.RFC3339))
	backupMutex.Unlock()
	
	log.Println("[Backup] Backup completed successfully.")
	return nil
}

func zipDirectory(source, target string) error {
	zipfile, err := os.Create(target)
	if err != nil {
		return err
	}
	defer zipfile.Close()

	archive := zip.NewWriter(zipfile)
	defer archive.Close()

	info, err := os.Stat(source)
	if err != nil {
		return nil // Source doesn't exist, skip
	}

	var baseDir string
	if info.IsDir() {
		baseDir = filepath.Base(source)
	}

	err = filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}

		if baseDir != "" {
			header.Name = filepath.Join(baseDir, strings.TrimPrefix(path, source))
		}

		header.Method = zip.Deflate

		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(writer, file)
		return err
	})

	return err
}

func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	newFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer newFile.Close()

	_, err = io.Copy(newFile, sourceFile)
	return err
}

func StartBackupTask(ctx context.Context, cfg *config.Config) {
	go func() {
		// Load last backup time from DB
		lastTimeStr := database.GetSetting("last_backup_time")
		if lastTimeStr != "" {
			t, err := time.Parse(time.RFC3339, lastTimeStr)
			if err == nil {
				backupMutex.Lock()
				lastBackupTime = t
				backupMutex.Unlock()
			}
		}

		// Initial wait to let system settle
		time.Sleep(1 * time.Minute)
		
		// Run every 24 hours
		ticker := time.NewTicker(1 * time.Minute) // Check every minute for more accurate scheduling
		defer ticker.Stop()
		
		for {
			now := time.Now()
			
			// Calculate next backup time based on last backup
			backupMutex.Lock()
			if lastBackupTime.IsZero() {
				// If never backed up, run after the 1-minute settle period
				nextBackupTime = now
			} else {
				nextBackupTime = lastBackupTime.Add(24 * time.Hour)
			}
			backupMutex.Unlock()

			if now.After(nextBackupTime) || now.Equal(nextBackupTime) {
				if database.GetSetting("backup_enabled") == "true" {
					PerformBackup(ctx, cfg)
				} else {
					// Even if disabled, we update the "virtual" last time to skip this cycle
					// so it doesn't try to run immediately when enabled later if long overdue
					// Or we can just let it run once when enabled.
					// Let's just update nextBackupTime display
				}
			}

			select {
			case <-ticker.C:
				// Continue to next check
			case <-ctx.Done():
				return
			}
		}
	}()
}
