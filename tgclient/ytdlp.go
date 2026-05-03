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
	"time"
)

func IsValidURL(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

var ytdlpSemaphore = make(chan struct{}, 2)

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
		// Limit error message length
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

	UpdateTaskWithFile(taskID, "downloading", 0, "waiting_slot", "", owner, 0, 0)

	// Wait for a slot in the ytdlp queue
	select {
	case ytdlpSemaphore <- struct{}{}:
		defer func() { <-ytdlpSemaphore }()
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
	ytdlpCtx, cancel := context.WithTimeout(ctx, 1*time.Hour)
	defer cancel()

	cmd := exec.CommandContext(ytdlpCtx, cfg.YTDLPPath, args...)

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
	// Merge stdout and stderr so progress lines and error messages are all captured
	combined := io.MultiReader(stdout, stderr)

	if err := cmd.Start(); err != nil {
		UpdateTaskWithFile(taskID, "error", 0, "start_error", "", owner, 0, 0)
		return
	}

	// Progress regex: [download]  10.0% of 100.00MiB at  1.00MiB/s ETA 01:30
	progressRegex := regexp.MustCompile(`\[download\]\s+(\d+\.\d+)%`)

	lastPercent := -1
	scanner := bufio.NewScanner(combined)
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

	if err := cmd.Wait(); err != nil {
		// If context was cancelled or timed out, it's not a real error
		if ctx.Err() != nil || ytdlpCtx.Err() != nil {
			statusMsg := "cancelled"
			if ytdlpCtx.Err() == context.DeadlineExceeded {
				statusMsg = "ytdlp_timeout"
			}
			UpdateTask(taskID, "error", 0, statusMsg, owner)
			return
		}
		UpdateTask(taskID, "error", 0, "ytdlp_failed", owner)
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
	for _, f := range files {
		if !f.IsDir() && strings.HasPrefix(f.Name(), prefix) {
			downloadedFile = filepath.Join(cfg.TempDir, f.Name())
			break
		}
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
