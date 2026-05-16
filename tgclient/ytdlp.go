package tgclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"telecloud/config"
	"telecloud/utils"
	"time"
)

func translateYTDLPError(errMsg string) string {
	errMsg = strings.ToLower(errMsg)
	
	switch {
	case strings.Contains(errMsg, "sign in to confirm your age"):
		return "age_restricted"
	case strings.Contains(errMsg, "incomplete youtube id") || strings.Contains(errMsg, "not a valid url"):
		return "invalid_url"
	case strings.Contains(errMsg, "this video is unavailable") || strings.Contains(errMsg, "video unavailable"):
		return "video_unavailable"
	case strings.Contains(errMsg, "private video"):
		return "video_private"
	case strings.Contains(errMsg, "403: forbidden") || strings.Contains(errMsg, "403 forbidden"):
		return "remote_forbidden"
	case strings.Contains(errMsg, "429: too many requests") || strings.Contains(errMsg, "429 too many requests"):
		return "too_many_requests"
	case strings.Contains(errMsg, "unsupported url"):
		return "unsupported_url"
	case strings.Contains(errMsg, "geo-restricted") || strings.Contains(errMsg, "not available in your country"):
		return "geo_restricted"
	case strings.Contains(errMsg, "ffmpeg not found"):
		return "ffmpeg_missing"
	case strings.Contains(errMsg, "getaddrinfo failed") || strings.Contains(errMsg, "name or service not known"):
		return "network_error"
	}
	
	return "ytdlp_error"
}

func IsValidURL(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}


// YTDLPInfo represents the structure of yt-dlp -J output
type YTDLPInfo struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Thumbnail   string        `json:"thumbnail"`
	Uploader    string        `json:"uploader"`
	Duration    float64       `json:"duration"`
	UploadDate  string        `json:"upload_date"`
	Formats     []YTDLPFormat `json:"formats"`
	Ext         string        `json:"ext"`
}

type YTDLPFormat struct {
	FormatID       string  `json:"format_id"`
	FormatNote     string  `json:"format_note"`
	Ext            string  `json:"ext"`
	Resolution     string  `json:"resolution"`
	Filesize       int64   `json:"filesize"`
	FilesizeApprox int64   `json:"filesize_approx"`
	VCodec         string  `json:"vcodec"`
	ACodec         string  `json:"acodec"`
	Format         string  `json:"format"`
	Height         int     `json:"height"`
}

func GetYTDLPFormats(url string, cfg *config.Config, owner string) (*YTDLPInfo, error) {
	ytdlpEnabled := cfg.YTDLPPath != "disabled" && cfg.YTDLPPath != "disable"
	ffmpegEnabled := cfg.FFMPEGPath != "disabled" && cfg.FFMPEGPath != "disable"
	if !ytdlpEnabled || !ffmpegEnabled {
		return nil, fmt.Errorf("ytdlp_disabled")
	}

	if !IsValidURL(url) {
		return nil, fmt.Errorf("invalid_url_format")
	}

	if utils.IsPrivateIP(url) {
		return nil, fmt.Errorf("forbidden_url")
	}

	args := []string{"-J", "--no-playlist", url}
	
	// Check for user cookie file
	cookieFile := filepath.Join(cfg.CookiesDir, fmt.Sprintf("user_%s.txt", owner))
	if _, err := os.Stat(cookieFile); err == nil {
		args = append([]string{"--cookies", cookieFile}, args...)
	}

	var stdout, stderr strings.Builder
	cmd := exec.Command(cfg.YTDLPPath, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = os.Environ()

	err := cmd.Run()
	if err != nil {
		// Clean up common yt-dlp error prefixes from stderr
		errMsg := stderr.String()
		if idx := strings.Index(errMsg, "ERROR:"); idx != -1 {
			errMsg = strings.TrimSpace(errMsg[idx+6:])
		}
		if errMsg == "" {
			errMsg = err.Error()
		}
		
		cleanErr := translateYTDLPError(errMsg)
		if cleanErr != "ytdlp_error" {
			return nil, fmt.Errorf("%s", cleanErr)
		}

		// Limit error message length for generic errors
		if len(errMsg) > 200 {
			errMsg = errMsg[:197] + "..."
		}
		return nil, fmt.Errorf("ytdlp_error: %s", errMsg)
	}

	var info YTDLPInfo
	if err := json.Unmarshal([]byte(stdout.String()), &info); err != nil {
		return nil, fmt.Errorf("json_unmarshal_error: %w", err)
	}

	// Filter duplicate formats (keep only one per unique height/resolution)
	if len(info.Formats) > 0 {
		filtered := make([]YTDLPFormat, 0)
		seenHeights := make(map[int]bool)

		for i := len(info.Formats) - 1; i >= 0; i-- {
			f := info.Formats[i]
			vcodec := strings.ToLower(f.VCodec)
			
			isAudio := vcodec == "none" || f.Resolution == "audio only"
			
			if isAudio {
				// For audio, use FormatID or combination of ext+filesize as key if needed,
				// but usually audio formats are distinct enough. 
				// Let's just keep them all for now or filter by ext if needed.
				filtered = append(filtered, f)
			} else if f.Height > 0 {
				if !seenHeights[f.Height] {
					filtered = append(filtered, f)
					seenHeights[f.Height] = true
				}
			} else {
				// Other formats (unknown height)
				filtered = append(filtered, f)
			}
		}
		// Reverse back to maintain original order (usually best quality last or first)
		for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
			filtered[i], filtered[j] = filtered[j], filtered[i]
		}
		info.Formats = filtered
	}

	return &info, nil
}

func ProcessYTDLPUpload(ctx context.Context, url, formatID, path, taskID, downloadType string, cfg *config.Config, owner string) {
	ytdlpEnabled := cfg.YTDLPPath != "disabled" && cfg.YTDLPPath != "disable"
	ffmpegEnabled := cfg.FFMPEGPath != "disabled" && cfg.FFMPEGPath != "disable"
	if !ytdlpEnabled || !ffmpegEnabled {
		UpdateTaskWithFile(taskID, "error", 0, "ytdlp_disabled", "", owner, 0, 0)
		return
	}

	if !IsValidURL(url) {
		UpdateTaskWithFile(taskID, "error", 0, "invalid_url_format", "", owner, 0, 0)
		return
	}

	if utils.IsPrivateIP(url) {
		UpdateTaskWithFile(taskID, "error", 0, "forbidden_url", "", owner, 0, 0)
		return
	}

	ctx, cancel := context.WithCancel(ctx)
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

	UpdateTaskWithFile(taskID, "waiting_slot", 0, "waiting_slot", "", owner, 0, 0)

	// Wait for a slot in the global download queue
	select {
	case globalDownloadSemaphore <- struct{}{}:
		defer func() { <-globalDownloadSemaphore }()
	case <-ctx.Done():
		UpdateTaskWithFile(taskID, "error", 0, "cancelled", "", owner, 0, 0)
		return
	}

	UpdateTaskWithFile(taskID, "downloading", 0, "initiating_ytdlp", "", owner, 0, 0)

	// Use a unique filename for the download to avoid collisions
	tempFileName := fmt.Sprintf("ytdlp_%s_%%(title)s.%%(ext)s", taskID)
	tempPathPattern := filepath.Join(cfg.TempDir, tempFileName)

	args := []string{
		"--newline",
		"--no-playlist",
		"-o", tempPathPattern,
	}

	// Audio conversion logic
	if downloadType == "audio" {
		args = append(args, "--extract-audio", "--audio-format", "mp3", "--embed-thumbnail", "--add-metadata", "--convert-thumbnails", "jpg")
	}

	// Check for user cookie file
	cookieFile := filepath.Join(cfg.CookiesDir, fmt.Sprintf("user_%s.txt", owner))
	if _, err := os.Stat(cookieFile); err == nil {
		args = append(args, "--cookies", cookieFile)
	}

	// Format selection flags must come BEFORE the URL
	if formatID != "" {
		switch downloadType {
		case "video":
			// Ensure video download includes audio if a specific video format is selected
			args = append(args, "-f", formatID+"+bestaudio/best", "--merge-output-format", "mp4")
		case "audio":
			// yt-dlp handles -f with --extract-audio correctly
			args = append(args, "-f", formatID)
		}
	} else {
		// Default smart selection based on type
		switch downloadType {
		case "audio":
			args = append(args, "-f", "bestaudio/best")
		default: // "video" (includes audio)
			args = append(args, "-f", "bestvideo+bestaudio/best", "--merge-output-format", "mp4")
		}
	}

	// URL must be the last argument
	args = append(args, url)

	// Create a context with a 1-hour timeout for the ytdlp process
	ytdlpCtx, cancelTimeout := context.WithTimeout(ctx, 1*time.Hour)
	defer cancelTimeout()

	cmd := exec.CommandContext(ytdlpCtx, cfg.YTDLPPath, args...)
	cmd.Env = os.Environ()
	setProcessGroup(cmd)

	// Capture both stdout and stderr interleaved for real-time progress
	var stderrBuf strings.Builder
	pr, pw := io.Pipe()
	
	// Create a tee for stderr so we can still capture the full error log
	stderrWriter := io.MultiWriter(pw, &stderrBuf)
	
	cmd.Stdout = pw
	cmd.Stderr = stderrWriter

	if err := cmd.Start(); err != nil {
		UpdateTaskWithFile(taskID, "error", 0, "start_error", "", owner, 0, 0)
		pw.Close()
		return
	}

	// Close the pipe writer when the command finishes to unblock the scanner
	go func() {
		_ = cmd.Wait()
		pw.Close()
	}()

	// Ensure whole process group is killed on context cancellation
	go func() {
		<-ytdlpCtx.Done()
		killProcessGroup(cmd)
	}()

	// Progress regex: [download]  10.0% of 100.00MiB at  1.00MiB/s ETA 01:30
	// Updated to handle both "10%" and "10.0%"
	progressRegex := regexp.MustCompile(`\[download\]\s+(\d+(?:\.\d+)?)%`)

	lastPercent := -1
	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		line := scanner.Text()
		matches := progressRegex.FindStringSubmatch(line)
		if len(matches) > 1 {
			percent, _ := strconv.ParseFloat(matches[1], 64)
			p := int(percent)
			if p != lastPercent {
				UpdateTask(taskID, "downloading", p, "downloading", owner)
				lastPercent = p
			}
		} else {
			// Clean informative messages
			if strings.HasPrefix(line, "[Merger] Merging formats") {
				UpdateTask(taskID, "downloading", 100, "ytdlp_merging", owner)
			} else if strings.Contains(line, "Adding thumbnail to") || strings.HasPrefix(line, "[EmbedThumbnail]") {
				UpdateTask(taskID, "downloading", 100, "ytdlp_thumbnail", owner)
			}
		}
	}

	// The wait is handled in the goroutine above, but we check if we actually finished correctly
	if ctx.Err() != nil || ytdlpCtx.Err() != nil {
		statusMsg := "cancelled"
		if ytdlpCtx.Err() == context.DeadlineExceeded {
			statusMsg = "ytdlp_timeout"
		}
		UpdateTask(taskID, "error", 0, statusMsg, owner)
		return
	}
	
	// Check if process failed after we've finished scanning
	// (cmd.ProcessState might be nil if Start failed, but we checked that)
	if cmd.ProcessState != nil && !cmd.ProcessState.Success() {
		errMsg := stderrBuf.String()
		if idx := strings.Index(errMsg, "ERROR:"); idx != -1 {
			errMsg = strings.TrimSpace(errMsg[idx+6:])
		}
		
		cleanErr := translateYTDLPError(errMsg)
		UpdateTask(taskID, "error", 0, cleanErr, owner)
		return
	}

	// Find the downloaded file
	files, err := os.ReadDir(cfg.TempDir)
	if err != nil {
		UpdateTask(taskID, "error", 0, "read_temp_dir_failed", owner)
		return
	}

	var downloadedFile string
	prefix := "ytdlp_" + taskID + "_"
	
	// Better selection: skip intermediate files like .part, .ytdl or .fXXX extensions
	var candidates []string
	for _, f := range files {
		if f.IsDir() || !strings.HasPrefix(f.Name(), prefix) {
			continue
		}
		
		name := f.Name()
		// Skip temporary/partial files
		if strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".ytdl") || strings.HasSuffix(name, ".temp") {
			continue
		}
		
		// Check for intermediate format extensions like .f248.webm or .f140.m4a
		isIntermediate := false
		parts := strings.Split(name, ".")
		for _, p := range parts {
			if strings.HasPrefix(p, "f") && len(p) > 1 {
				if _, err := strconv.Atoi(p[1:]); err == nil {
					isIntermediate = true
					break
				}
			}
		}
		
		if !isIntermediate {
			downloadedFile = filepath.Join(cfg.TempDir, name)
			break
		}
		candidates = append(candidates, filepath.Join(cfg.TempDir, name))
	}

	// Fallback to first candidate if no "clean" file found
	if downloadedFile == "" && len(candidates) > 0 {
		downloadedFile = candidates[0]
	}

	if downloadedFile == "" {
		UpdateTask(taskID, "error", 0, "downloaded_file_not_found", owner)
		return
	}

	// Ensure cleanup
	defer os.Remove(downloadedFile)

	// Prepare for upload
	filename := filepath.Base(downloadedFile)
	// Strip the ytdlp_taskID_ prefix
	filename = strings.TrimPrefix(filename, prefix)

	// Refine MIME type based on actual extension
	mimeType := "application/octet-stream"
	ext := filepath.Ext(filename)
	if ext != "" {
		mimeType = mime.TypeByExtension(ext)
	}

	// Fallback for common types if mime package is incomplete
	if mimeType == "" || mimeType == "application/octet-stream" {
		switch strings.ToLower(ext) {
		case ".mp4", ".m4v":
			mimeType = "video/mp4"
		case ".webm":
			mimeType = "video/webm"
		case ".mkv":
			mimeType = "video/x-matroska"
		case ".mp3":
			mimeType = "audio/mpeg"
		case ".m4a":
			mimeType = "audio/mp4"
		case ".ogg", ".opus":
			mimeType = "audio/ogg"
		}
	}

	// Call existing upload logic
	ProcessCompleteUpload(ctx, downloadedFile, filename, path, mimeType, taskID, cfg, false, owner)
}
