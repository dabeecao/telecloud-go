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
	"telecloud/utils"
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
	
	tempDir := filepath.Join(cfg.TempDir, "backups_temp")
	os.MkdirAll(tempDir, 0755)
	defer os.RemoveAll(tempDir)

	timestamp := time.Now().Format("20060102_150405")
	bundleDir := filepath.Join(tempDir, fmt.Sprintf("telecloud_backup_%s", timestamp))
	os.MkdirAll(bundleDir, 0755)

	var filesToBundle []string

	// 1. Database Backup (SQLite only)
	if database.IsSQLite() {
		dbBackupPath := filepath.Join(bundleDir, "database.db")
		_, err := database.DB.Exec(fmt.Sprintf("VACUUM INTO '%s'", dbBackupPath))
		if err != nil {
			log.Printf("[Backup] VACUUM INTO failed, falling back to copy: %v", err)
			err = copyFile(cfg.DatabasePath, dbBackupPath)
		}
		if err == nil {
			filesToBundle = append(filesToBundle, dbBackupPath)
		} else {
			log.Printf("[Backup] Failed to backup database: %v", err)
		}
	}

	// 2. Thumbnails
	thumbsSourceDir := cfg.ThumbsDir
	if _, err := os.Stat(thumbsSourceDir); err == nil {
		thumbsDestDir := filepath.Join(bundleDir, "thumbnails")
		os.MkdirAll(thumbsDestDir, 0755)
		// We'll zip the whole bundle later, so just copy files or structure
		// To keep it efficient, we'll just zip thumbnails separately or just include them in the main zip
	}

	// 3. Create the final bundle ZIP
	finalZipPath := filepath.Join(tempDir, fmt.Sprintf("telecloud_backup_%s.zip", timestamp))
	err := zipBackupBundle(cfg.DatabasePath, cfg.ThumbsDir, finalZipPath)
	if err != nil {
		log.Printf("[Backup] Failed to create backup bundle: %v", err)
		backupMutex.Lock()
		lastBackupStatus = "failed: zip_error"
		database.SetSetting("last_backup_status", lastBackupStatus)
		backupMutex.Unlock()
		return err
	}

	// Check size
	stat, _ := os.Stat(finalZipPath)
	if stat.Size() > cfg.MaxPartSize {
		log.Printf("[Backup] Backup file is too large (%d bytes), limit is %d bytes", stat.Size(), cfg.MaxPartSize)
		backupMutex.Lock()
		lastBackupStatus = "failed: file_too_large"
		database.SetSetting("last_backup_status", lastBackupStatus)
		backupMutex.Unlock()
		return fmt.Errorf("file too large")
	}

	// 4. Upload to Telegram Log Group
	mainApi := GetAPI()
	peer, err := resolveLogGroup(ctx, mainApi, cfg.LogGroupID)
	if err != nil {
		backupMutex.Lock()
		lastBackupStatus = "failed: could not resolve log group"
		database.SetSetting("last_backup_status", lastBackupStatus)
		backupMutex.Unlock()
		return err
	}

	sender := message.NewSender(mainApi)
	up := uploader.NewUploader(mainApi).WithThreads(cfg.UploadThreads)

	log.Printf("[Backup] Uploading backup bundle (%s)...", utils.FormatBytes(stat.Size()))
	
	f, err := os.Open(finalZipPath)
	if err != nil {
		return err
	}
	defer f.Close()

	tgFile, err := up.FromReader(ctx, filepath.Base(finalZipPath), io.LimitReader(f, stat.Size()))
	if err != nil {
		log.Printf("[Backup] Failed to upload: %v", err)
		backupMutex.Lock()
		lastBackupStatus = "failed: upload_error"
		database.SetSetting("last_backup_status", lastBackupStatus)
		backupMutex.Unlock()
		return err
	}

	caption := fmt.Sprintf("<b>📦 TeleCloud Automated Backup</b>\n\n<b>File:</b> %s\n<b>Size:</b> %s\n<b>Date:</b> %s\n\n#backup", 
		filepath.Base(finalZipPath), utils.FormatBytes(stat.Size()), time.Now().Format("2006-01-02 15:04:05"))
	
	docBuilder := message.UploadedDocument(tgFile, html.String(nil, caption)).Filename(filepath.Base(finalZipPath))
	_, err = sender.To(peer).Media(ctx, docBuilder)
	if err != nil {
		log.Printf("[Backup] Failed to send to group: %v", err)
		backupMutex.Lock()
		lastBackupStatus = "failed: telegram_send_error"
		database.SetSetting("last_backup_status", lastBackupStatus)
		backupMutex.Unlock()
		return err
	}

	backupMutex.Lock()
	lastBackupTime = time.Now()
	lastBackupStatus = "success"
	database.SetSetting("last_backup_time", lastBackupTime.Format(time.RFC3339))
	database.SetSetting("last_backup_status", lastBackupStatus)
	backupMutex.Unlock()
	
	log.Println("[Backup] Backup completed successfully.")
	return nil
}

func zipBackupBundle(dbPath, thumbsDir, targetZip string) error {
	zipfile, err := os.Create(targetZip)
	if err != nil {
		return err
	}
	defer zipfile.Close()

	archive := zip.NewWriter(zipfile)
	defer archive.Close()

	// 1. Add Database
	if dbPath != "" {
		if database.IsSQLite() {
			// For SQLite, we should use a temporary copy to avoid "database is locked" during zip
			tempDB := targetZip + ".db"
			_, err := database.DB.Exec(fmt.Sprintf("VACUUM INTO '%s'", tempDB))
			if err != nil {
				// Fallback to direct copy if VACUUM fails
				err = copyFile(dbPath, tempDB)
			}
			
			if err == nil {
				defer os.Remove(tempDB)
				f, err := os.Open(tempDB)
				if err == nil {
					w, err := archive.Create("database.db")
					if err == nil {
						io.Copy(w, f)
					}
					f.Close()
				}
			}
		}
	}

	// 2. Add Thumbnails
	if thumbsDir != "" {
		_ = filepath.Walk(thumbsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			
			relPath, err := filepath.Rel(thumbsDir, path)
			if err != nil {
				return nil
			}

			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			header.Name = filepath.Join("thumbnails", relPath)
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
	}

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
		// Load last backup time and status from DB
		lastTimeStr := database.GetSetting("last_backup_time")
		if lastTimeStr != "" {
			t, err := time.Parse(time.RFC3339, lastTimeStr)
			if err == nil {
				backupMutex.Lock()
				lastBackupTime = t
				backupMutex.Unlock()
			}
		}

		lastStatus := database.GetSetting("last_backup_status")
		if lastStatus != "" {
			backupMutex.Lock()
			lastBackupStatus = lastStatus
			backupMutex.Unlock()
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
