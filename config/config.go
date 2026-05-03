package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	APIID           int
	APIHash         string
	UploadThreads   int
	DatabasePath    string
	ThumbsDir       string
	LogGroupID      string
	Port            string
	TempDir         string
	ProxyURL        string
	Version         string
	SessionFile     string
	FFMPEGPath      string
	YTDLPPath       string
	WebAuthnRPID     string
	WebAuthnRPOrigin   string
	MaxPartSize      int64
	CookiesDir       string
	IsPremium        bool
	Warnings         []string
}

func Load() (*Config, error) {
	var warnings []string
	err := godotenv.Load()
	if err != nil && !os.IsNotExist(err) {
		warnings = append(warnings, "Error loading .env file: "+err.Error())
	}

	apiID, _ := strconv.Atoi(os.Getenv("API_ID"))
	apiHash := os.Getenv("API_HASH")

	if apiID == 0 || apiHash == "" {
		return nil, fmt.Errorf("Error: API_ID and API_HASH must be set in .env. Please get them from https://my.telegram.org")
	}

	uploadThreads, _ := strconv.Atoi(getEnv("TG_UPLOAD_THREADS", "2"))
	if uploadThreads <= 0 {
		uploadThreads = 2
	}

	logGroupID := os.Getenv("LOG_GROUP_ID")

	// MaxPartSize will be auto-detected in tgclient based on account status (Premium/Regular)
	maxPartSizeMB := int64(1900) 

	ffmpegPath := getEnv("FFMPEG_PATH", "ffmpeg")
	if ffmpegPath != "disabled" && ffmpegPath != "disable" {
		if !isExecutable(ffmpegPath) {
			warnings = append(warnings, "WARNING: FFMPEG path '"+ffmpegPath+"' not found or not executable. Disabling FFMPEG support.")
			ffmpegPath = "disabled"
		}
	}

	ytdlpPath := getEnv("YTDLP_PATH", "disabled")
	if ytdlpPath != "disabled" && ytdlpPath != "disable" {
		if !isExecutable(ytdlpPath) {
			warnings = append(warnings, "WARNING: YT-DLP path '"+ytdlpPath+"' not found or not executable. Disabling YT-DLP support.")
			ytdlpPath = "disabled"
		}
	}

	return &Config{
		APIID:           apiID,
		APIHash:         apiHash,
		UploadThreads:   uploadThreads,
		DatabasePath:    getEnv("DATABASE_PATH", "database.db"),
		ThumbsDir:       getEnv("THUMBS_DIR", "static/thumbs"),
		LogGroupID:      logGroupID,
		Port:            getEnv("PORT", "8091"),
		TempDir:         getEnv("TEMP_DIR", filepath.Join(os.TempDir(), "telecloud_temp_chunks")),
		ProxyURL:        getEnv("PROXY_URL", ""),
		SessionFile:     getEnv("SESSION_FILE", "session.json"),
		FFMPEGPath:      ffmpegPath,
		YTDLPPath:       ytdlpPath,
		WebAuthnRPID:     getEnv("WEBAUTHN_RPID", "localhost"),
		WebAuthnRPOrigin:   getEnv("WEBAUTHN_RPORIGIN", "http://localhost:8091"),
		MaxPartSize:      maxPartSizeMB * 1024 * 1024,
		CookiesDir:       getEnv("COOKIES_DIR", "data/cookies"),
		Warnings:         warnings,
	}, nil
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func isExecutable(path string) bool {
	// On Windows and macOS, the standard LookPath is safe and handles 
	// platform-specific nuances (like .exe extensions) perfectly.
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		_, err := exec.LookPath(path)
		return err == nil
	}

	// On Linux (including Android/Termux), we use manual PATH search
	// to avoid the faccessat2 syscall which triggers SIGSYS on older/restricted kernels.
	if strings.Contains(path, string(os.PathSeparator)) {
		return checkFileExecutable(path)
	}

	pathEnv := os.Getenv("PATH")
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		fullPath := filepath.Join(dir, path)
		if checkFileExecutable(fullPath) {
			return true
		}
	}

	return false
}

func checkFileExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	// Check if it's a regular file and has any executable bit set (0111 is --x--x--x)
	return !info.IsDir() && (info.Mode().Perm()&0111 != 0)
}
