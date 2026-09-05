package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/joho/godotenv"

	"telecloud/utils"
)

type Config struct {
	APIID            int
	APIHash          string
	UploadThreads    int
	MaxParallelParts int
	MaxConcurrentUps int
	DownloadPrefetch int
	DatabaseDriver   string
	DatabasePath     string
	DatabaseDSN      string
	ThumbsDir        string
	LogGroupID       string
	Port             string
	TempDir          string
	ProxyURL         string
	Version          string
	FFMPEGPath       string
	YTDLPPath        string
	WebAuthnRPID     string
	WebAuthnRPOrigin string
	MaxPartSize      int64
	CookiesDir       string
	IsPremium        bool
	BotTokens        []string
	Warnings         []string
	TorrentEnabled   bool
	TorrentPath      string
	ListenAddr       string
}

func Load() (*Config, error) {
	var warnings []string
	err := godotenv.Load()
	if err != nil && !os.IsNotExist(err) {
		warnings = append(warnings, "Error loading .env file: "+err.Error())
	}

	if _, err := utils.LoadMasterKey(); err != nil {
		return nil, err
	}

	var apiID int
	var apiHash string

	uploadThreads, _ := strconv.Atoi(getEnv("TG_UPLOAD_THREADS", "2"))
	if uploadThreads <= 0 {
		uploadThreads = 2
	}

	// Upload concurrency caps. Both default to 0, which keeps the historical
	// behaviour of deriving the limit from the bot-pool size. Set them when the
	// network path to Telegram cannot sustain many simultaneous connections —
	// on links that reset long high-throughput streams, 1/1 trades speed for
	// actually finishing. Negative values are ignored.
	maxParallelParts, _ := strconv.Atoi(getEnv("TG_MAX_PARALLEL_PARTS", "0"))
	if maxParallelParts < 0 {
		maxParallelParts = 0
	}
	maxConcurrentUps, _ := strconv.Atoi(getEnv("TG_MAX_CONCURRENT_UPLOADS", "0"))
	if maxConcurrentUps < 0 {
		maxConcurrentUps = 0
	}

	// Number of 1MB chunks fetched ahead of the read cursor while streaming.
	downloadPrefetch, _ := strconv.Atoi(getEnv("TG_DOWNLOAD_PREFETCH", "4"))
	if downloadPrefetch <= 0 {
		downloadPrefetch = 4
	}
	if downloadPrefetch > 16 {
		downloadPrefetch = 16
	}

	logGroupID := os.Getenv("LOG_GROUP_ID")

	// MaxPartSize will be auto-detected in tgclient based on account status (Premium/Regular)
	maxPartSizeMB := int64(1900)

	ffmpegPath := getEnv("FFMPEG_PATH", "ffmpeg")
	if ffmpegPath != "disabled" && ffmpegPath != "disable" {
		resolvedPath, ok := findExecutable(ffmpegPath)
		if !ok {
			warnings = append(warnings, "WARNING: FFMPEG path '"+ffmpegPath+"' not found or not executable. Disabling FFMPEG support.")
			ffmpegPath = "disabled"
		} else {
			ffmpegPath = resolvedPath
		}
	}

	ytdlpPath := getEnv("YTDLP_PATH", "disabled")
	if ytdlpPath != "disabled" && ytdlpPath != "disable" {
		resolvedPath, ok := findExecutable(ytdlpPath)
		if !ok {
			warnings = append(warnings, "WARNING: YT-DLP path '"+ytdlpPath+"' not found or not executable. Disabling YT-DLP support.")
			ytdlpPath = "disabled"
		} else {
			ytdlpPath = resolvedPath
		}
	}

	torrentPath := getEnv("TORRENT_PATH", "disabled")
	torrentEnabled := false
	if torrentPath != "disabled" && torrentPath != "disable" {
		resolvedPath, ok := findExecutable(torrentPath)
		if !ok {
			warnings = append(warnings, "WARNING: TORRENT_PATH '"+torrentPath+"' not found or not executable. Disabling torrent support.")
			torrentPath = "disabled"
		} else {
			torrentPath = resolvedPath
			torrentEnabled = true
		}
	}

	return &Config{
		APIID:            apiID,
		APIHash:          apiHash,
		UploadThreads:    uploadThreads,
		MaxParallelParts: maxParallelParts,
		MaxConcurrentUps: maxConcurrentUps,
		DownloadPrefetch: downloadPrefetch,
		DatabaseDriver:   strings.ToLower(getEnv("DATABASE_DRIVER", "sqlite")),
		DatabasePath:     getEnv("DATABASE_PATH", "database.db"),
		DatabaseDSN:      getEnv("DATABASE_DSN", ""),
		ThumbsDir:        getEnv("THUMBS_DIR", "static/thumbs"),
		LogGroupID:       logGroupID,
		Port:             getEnv("PORT", "8091"),
		TempDir:          getEnv("TEMP_DIR", filepath.Join(os.TempDir(), "telecloud_temp_chunks")),
		ProxyURL:         getEnv("PROXY_URL", ""),
		FFMPEGPath:       ffmpegPath,
		YTDLPPath:        ytdlpPath,
		WebAuthnRPID:     getEnv("WEBAUTHN_RPID", "localhost"),
		WebAuthnRPOrigin: getEnv("WEBAUTHN_RPORIGIN", "http://localhost:8091"),
		MaxPartSize:      maxPartSizeMB * 1024 * 1024,
		CookiesDir:       getEnv("COOKIES_DIR", "data/cookies"),
		BotTokens:        nil,
		Warnings:         warnings,
		TorrentEnabled:   torrentEnabled,
		TorrentPath:      torrentPath,
		ListenAddr:       getEnv("LISTEN_ADDR", ""),
	}, nil
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

// findExecutable locates the binary and returns its absolute path and true if found.
func findExecutable(path string) (string, bool) {
	// On Windows and macOS, the standard LookPath is safe and handles
	// platform-specific nuances (like .exe extensions) perfectly.
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		absPath, err := exec.LookPath(path)
		if err != nil {
			return "", false
		}
		return absPath, true
	}

	// On Linux (including Android/Termux), we use manual PATH search
	// to avoid the faccessat2 syscall which triggers SIGSYS on older/restricted kernels.
	if strings.Contains(path, string(os.PathSeparator)) {
		if checkFileExecutable(path) {
			abs, _ := filepath.Abs(path)
			return abs, true
		}
		return "", false
	}

	pathEnv := os.Getenv("PATH")
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		fullPath := filepath.Join(dir, path)
		if checkFileExecutable(fullPath) {
			abs, _ := filepath.Abs(fullPath)
			return abs, true
		}
	}

	return "", false
}

func checkFileExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	// Check if it's a regular file and has any executable bit set (0111 is --x--x--x)
	return !info.IsDir() && (info.Mode().Perm()&0111 != 0)
}

var (
	DefaultAPIIDStr = "0"
	DefaultAPIHash  = ""
)

func (c *Config) LoadFromDB(getSettingFunc func(key string) string) {
	if c.APIID == 0 {
		apiIDStr := getSettingFunc("api_id")
		if apiIDStr != "" {
			c.APIID, _ = strconv.Atoi(apiIDStr)
		} else if DefaultAPIIDStr != "" && DefaultAPIIDStr != "0" {
			c.APIID, _ = strconv.Atoi(DefaultAPIIDStr)
		}
	}
	if c.APIHash == "" {
		dbHash := getSettingFunc("api_hash")
		if dbHash != "" {
			c.APIHash = dbHash
		} else {
			c.APIHash = DefaultAPIHash
		}
	}
	if c.LogGroupID == "" {
		c.LogGroupID = getSettingFunc("log_group_id")
	}
	dbBotTokens := getSettingFunc("bot_tokens")
	if dbBotTokens != "" {
		var tokens []string
		for _, t := range strings.Split(dbBotTokens, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tokens = append(tokens, t)
			}
		}
		c.BotTokens = tokens
	} else {
		c.BotTokens = nil
	}
}
