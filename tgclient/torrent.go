package tgclient

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"telecloud/config"
	"telecloud/database"
	"telecloud/utils"
	"time"
)

// IsValidTorrentInput validates magnet links and .torrent URLs.
func IsValidTorrentInput(input string) bool {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "magnet:") {
		return true
	}
	if (strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://")) &&
		strings.HasSuffix(strings.ToLower(strings.Split(input, "?")[0]), ".torrent") {
		return true
	}
	return false
}

// ProcessTorrentUpload downloads a torrent (magnet or .torrent URL) via aria2c
// and uploads the resulting file(s) to Telegram.
// - Single-file torrent: uploads to `path` directly.
// - Multi-file torrent: creates a sub-folder named after the torrent, uploads all files inside.
func ProcessTorrentUpload(ctx context.Context, input, path, taskID string, cfg *config.Config, owner string) {
	UpdateTask(taskID, "waiting_slot", 0, "waiting_slot", owner)

	// Wait for a slot in the global download queue
	select {
	case globalDownloadSemaphore <- struct{}{}:
		defer func() { <-globalDownloadSemaphore }()
	case <-ctx.Done():
		return
	}

	UpdateTask(taskID, "downloading", 0, "initiating_torrent", owner)

	// Create a unique temp sub-directory for this download
	torrentTempDir := filepath.Join(cfg.TempDir, "torrent_"+taskID)
	var err error
	for retry := 0; retry < 3; retry++ {
		err = os.MkdirAll(torrentTempDir, 0755)
		if err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}

	if err != nil {
		UpdateTaskWithFile(taskID, "error", 0, "torrent_temp_dir_failed", "", owner, 0, 0)
		return
	}

	torrentCtx, cancel := context.WithTimeout(ctx, 48*time.Hour)
	defer cancel()

	// Register for cancellation
	taskMutex.Lock()
	TaskCancels[taskID] = cancel
	taskMutex.Unlock()
	defer func() {
		taskMutex.Lock()
		delete(TaskCancels, taskID)
		taskMutex.Unlock()
	}()
	defer os.RemoveAll(torrentTempDir)

	// Build aria2c arguments
	args := []string{
		"--enable-rpc=false",
		"--daemon=false",
		"--seed-time=0",
		"--summary-interval=1",
		"--show-console-readout=true",
		"--dir=" + torrentTempDir,
		"--max-connection-per-server=4",
		"--split=4",
		"--min-split-size=1M",
		"--console-log-level=notice",
		"--download-result=default",
		"--file-allocation=none",
		"--check-integrity=true",
		input,
	}

	// 2-hour timeout for the aria2c process
	torrentCtx, cancelTimeout := context.WithTimeout(ctx, 2*time.Hour)
	defer cancelTimeout()

	cmd := exec.CommandContext(torrentCtx, cfg.TorrentPath, args...)
	cmd.Env = os.Environ()
	setProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		UpdateTaskWithFile(taskID, "error", 0, "pipe_error", "", owner, 0, 0)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		UpdateTaskWithFile(taskID, "error", 0, "pipe_error", "", owner, 0, 0)
		return
	}

	if err := cmd.Start(); err != nil {
		UpdateTaskWithFile(taskID, "error", 0, "start_error", "", owner, 0, 0)
		return
	}

	// Kill whole process group on context cancellation
	go func() {
		<-torrentCtx.Done()
		killProcessGroup(cmd)
	}()

	// aria2c progress line example:
	//  [#abc123 1.2GiB/4.5GiB(26%) CN:4 DL:5.2MiB ETA:10m]
	// Flexible to match both summary and live readout formats
	progressRegex := regexp.MustCompile(`\[#([0-9a-f]+)\s+.*?([0-9.]+[KMG]?i?B)/([0-9.]+[KMG]?i?B)(?:\((\d+)%\))?`)
	speedRegex := regexp.MustCompile(`(?i)DL:([0-9.]+[KMG]?i?B)`)
	// Capture torrent name from: 
	// 1. NOTICE: ... 'Name' OR complete: Name
	// 2. [#abc GID Name|...]
	// 3. FILE: /path/to/Name
	torrentNameRegex := regexp.MustCompile(`(?i)(?:NOTICE:.*(?:'(.+?)'|complete:\s+(.+))|\[#[0-9a-f]+\s+([^|\]]+)\||^FILE:\s+(.+))`)

	lastPercent := -1
	lastTotalSize := int64(0)
	lastGID := ""
	torrentName := "Torrent"

	// Custom split function to handle both \n and \r (aria2c progress updates use \r)
	splitFunc := func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}
		for i, b := range data {
			if b == '\n' || b == '\r' {
				return i + 1, data[0:i], nil
			}
		}
		if atEOF {
			return len(data), data, nil
		}
		return 0, nil, nil
	}

	// Scan both stdout and stderr
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Split(splitFunc)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "is a torrent file") || strings.Contains(line, "Downloading") || strings.Contains(line, "NOTICE") || strings.HasPrefix(line, "FILE:") {
				if m := torrentNameRegex.FindStringSubmatch(line); len(m) > 1 {
					name := m[1]
					if name == "" && len(m) > 2 {
						name = m[2]
					}
					if name == "" && len(m) > 3 {
						name = m[3]
					}
					if name == "" && len(m) > 4 {
						name = m[4]
						// Path cleaning: try to get relative folder name if it's an absolute path
						if strings.Contains(name, string(filepath.Separator)) {
							rel, err := filepath.Rel(torrentTempDir, name)
							if err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
								parts := strings.Split(rel, string(filepath.Separator))
								if len(parts) > 0 {
									name = parts[0]
								}
							} else {
								name = filepath.Base(name)
							}
						}
					}
					if name != "" {
						name = strings.TrimSpace(name)
						// Remove common aria2c tags from summary name
						name = strings.TrimPrefix(name, "[MEMORY]")
						name = strings.TrimPrefix(name, "[METADATA]")
						name = strings.TrimSpace(name)
						name = strings.TrimSuffix(name, ".torrent")
						name = strings.TrimSuffix(name, ".TORRENT")
						if torrentName == "Torrent" || len(name) > len(torrentName) {
							torrentName = name
						}
					}
				}
			}
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Split(splitFunc)
	for scanner.Scan() {
		line := scanner.Text()

		// Parse name from progress line if not yet found
		if m := torrentNameRegex.FindStringSubmatch(line); len(m) > 1 {
			name := m[1]
			if name == "" && len(m) > 2 {
				name = m[2]
			}
			if name == "" && len(m) > 3 {
				name = m[3]
			}
			if name == "" && len(m) > 4 {
				name = m[4]
				if strings.Contains(name, string(filepath.Separator)) {
					rel, err := filepath.Rel(torrentTempDir, name)
					if err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
						parts := strings.Split(rel, string(filepath.Separator))
						if len(parts) > 0 {
							name = parts[0]
						}
					} else {
						name = filepath.Base(name)
					}
				}
			}
			if name != "" {
				name = strings.TrimSpace(name)
				name = strings.TrimPrefix(name, "[MEMORY]")
				name = strings.TrimPrefix(name, "[METADATA]")
				name = strings.TrimSpace(name)
				name = strings.TrimSuffix(name, ".torrent")
				name = strings.TrimSuffix(name, ".TORRENT")
				if torrentName == "Torrent" || (len(name) > len(torrentName) && !strings.Contains(name, "#")) {
					torrentName = name
				}
			}
		}

		// Parse percent, speed and size
		if m := progressRegex.FindStringSubmatch(line); len(m) > 3 {
			gid := m[1]
			downloadedStr := m[2]
			totalStr := m[3]
			
			p := 0
			if len(m) > 4 && m[4] != "" {
				p, _ = strconv.Atoi(m[4])
			}

			speedStr := ""
			if sm := speedRegex.FindStringSubmatch(line); len(sm) > 1 {
				speedStr = sm[1]
			}

			totalSizeBytes := parseSpeedToBytes(totalStr)
			downloadedBytes := parseSpeedToBytes(downloadedStr)

			// Manual percentage calculation if missing
			if len(m) <= 4 || m[4] == "" {
				if totalSizeBytes > 0 {
					p = int((float64(downloadedBytes) / float64(totalSizeBytes)) * 100)
				}
			}

			// Handle GID change (transition from metadata to torrent)
			if gid != lastGID {
				if lastGID != "" {
					// We transitioned, reset progress reporting for the new file
					lastPercent = -1
					// CRITICAL: Reset lastTotalSize because GID #2 is a different download task
					lastTotalSize = 0 
				}
				lastGID = gid
			}

			// Ignore metadata/control file downloads (usually < 1MB) if we are expecting a real torrent
			if totalSizeBytes > 0 && totalSizeBytes < 1024*1024 && p == 100 {
				continue
			}

			// Safety: Only update if the new total size is greater than or equal to the last seen total size
			if totalSizeBytes > 0 {
				if totalSizeBytes < lastTotalSize {
					continue
				}

				// Check for disk space if size is significant (> 1MB)
				// We check when size is first detected or increases
				if totalSizeBytes > 1024*1024 && totalSizeBytes != lastTotalSize {
					_, free, err := utils.GetDiskSpace(torrentTempDir)
					if err == nil && uint64(totalSizeBytes) > free {
						killProcessGroup(cmd)
						UpdateTask(taskID, "error", 0, "err_insufficient_storage", owner)
						return
					}
				}

				lastTotalSize = totalSizeBytes
			}

			if p != lastPercent || speedStr != "" {
				msg := "torrent_downloading"
				speedBytes := parseSpeedToBytes(speedStr)

				UpdateTaskWithSpeed(taskID, "downloading", p, msg, torrentName, owner, totalSizeBytes, downloadedBytes, speedBytes)
				lastPercent = p
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil || torrentCtx.Err() != nil {
			statusMsg := "cancelled"
			if torrentCtx.Err() == context.DeadlineExceeded {
				statusMsg = "torrent_timeout"
			}
			UpdateTask(taskID, "error", 0, statusMsg, owner)
			return
		}
		UpdateTask(taskID, "error", 0, "torrent_download_failed", owner)
		return
	}

	// ── Collect downloaded files ──────────────────────────────────────────────
	var allFiles []string
	if err := filepath.WalkDir(torrentTempDir, func(p string, d fs.DirEntry, e error) error {
		if e != nil || d.IsDir() {
			return nil
		}
		// Ignore metadata and control files
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".torrent" || ext == ".aria2" {
			return nil
		}
		allFiles = append(allFiles, p)
		return nil
	}); err != nil || len(allFiles) == 0 {
		UpdateTask(taskID, "error", 0, "torrent_no_files", owner)
		return
	}

	// ── Determine destination path ────────────────────────────────────────────
	// Multi-file: create a sub-folder named after the torrent.
	// Single-file: upload directly to `path`.
	destPath := path
	if len(allFiles) > 1 {
		// Derive folder name from torrent name, or fall back to taskID
		folderName := torrentName
		if folderName == "" {
			// Try to use the name of the first directory inside torrentTempDir
			entries, _ := os.ReadDir(torrentTempDir)
			for _, e := range entries {
				if e.IsDir() {
					folderName = e.Name()
					break
				}
			}
		}
		if folderName == "" {
			folderName = "torrent_" + taskID
		}
		folderName = sanitizeFolderName(folderName)
		destPath = pathpkg.Join(path, folderName)
		database.EnsureFoldersExist(destPath, owner)
	}

	// Mark parent task as done now that it has split into sub-tasks
	UpdateTask(taskID, "done", 100, "torrent_finished", owner)

	total := len(allFiles)
	for i, filePath := range allFiles {
		if ctx.Err() != nil {
			return
		}

		filename := filepath.Base(filePath)
		subTaskID := fmt.Sprintf("%s_%d", taskID, i)

		// Determine destination path
		subDestPath := destPath
		if total > 1 {
			rel, err := filepath.Rel(torrentTempDir, filePath)
			if err == nil {
				// rel usually starts with torrentName folder if aria2c created it
				parts := strings.SplitN(rel, string(filepath.Separator), 2)
				if len(parts) == 2 {
					subDir := filepath.Dir(parts[1])
					if subDir != "." {
						subDestPath = database.JoinPath(destPath, filepath.ToSlash(subDir))
						database.EnsureFoldersExist(subDestPath, owner)
					}
				}
			}
		}

		// Initial sub-task report
		stat, _ := os.Stat(filePath)
		fSize := int64(0)
		if stat != nil {
			fSize = stat.Size()
		}

		msg := "pushing_to_tg"
		if total > 1 {
			msg = fmt.Sprintf("uploading_file_x_of_y|x=%d,y=%d", i+1, total)
		}

		// Register the sub-task with its name and size so it appears immediately
		UpdateTaskWithSpeed(subTaskID, "telegram", 0, msg, filename, owner, fSize, 0, 0)

		ProcessCompleteUpload(ctx, filePath, filename, subDestPath, detectMIME(filename), subTaskID, cfg, false, owner)
	}
}

// sanitizeFolderName strips characters that are invalid in folder names.
func sanitizeFolderName(name string) string {
	// Replace path separators and null bytes
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "\x00", "")
	name = strings.TrimSpace(name)
	if name == "" {
		return "torrent"
	}
	return name
}

// detectMIME returns a best-effort MIME type based on file extension.
func detectMIME(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	mimeMap := map[string]string{
		".mp4":  "video/mp4",
		".mkv":  "video/x-matroska",
		".avi":  "video/x-msvideo",
		".mov":  "video/quicktime",
		".webm": "video/webm",
		".mp3":  "audio/mpeg",
		".m4a":  "audio/mp4",
		".flac": "audio/flac",
		".ogg":  "audio/ogg",
		".opus": "audio/ogg",
		".wav":  "audio/wav",
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".png":  "image/png",
		".gif":  "image/gif",
		".webp": "image/webp",
		".pdf":  "application/pdf",
		".zip":  "application/zip",
		".rar":  "application/x-rar-compressed",
		".7z":   "application/x-7z-compressed",
		".tar":  "application/x-tar",
		".gz":   "application/gzip",
	}
	if m, ok := mimeMap[ext]; ok {
		return m
	}
	return "application/octet-stream"
}

// parseSpeedToBytes converts aria2c speed strings (e.g., "5.2MiB", "100KiB") to bytes per second.
func parseSpeedToBytes(speedStr string) int64 {
	speedStr = strings.ToUpper(strings.TrimSpace(speedStr))
	if speedStr == "" || speedStr == "0B" {
		return 0
	}

	multiplier := int64(1)
	if strings.HasSuffix(speedStr, "KIB") || strings.HasSuffix(speedStr, "KB") {
		multiplier = 1024
		speedStr = strings.TrimSuffix(strings.TrimSuffix(speedStr, "KIB"), "KB")
	} else if strings.HasSuffix(speedStr, "MIB") || strings.HasSuffix(speedStr, "MB") {
		multiplier = 1024 * 1024
		speedStr = strings.TrimSuffix(strings.TrimSuffix(speedStr, "MIB"), "MB")
	} else if strings.HasSuffix(speedStr, "GIB") || strings.HasSuffix(speedStr, "GB") {
		multiplier = 1024 * 1024 * 1024
		speedStr = strings.TrimSuffix(strings.TrimSuffix(speedStr, "GIB"), "GB")
	} else if strings.HasSuffix(speedStr, "B") {
		speedStr = strings.TrimSuffix(speedStr, "B")
	}

	val, err := strconv.ParseFloat(speedStr, 64)
	if err != nil {
		return 0
	}
	return int64(val * float64(multiplier))
}
