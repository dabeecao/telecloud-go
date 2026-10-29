package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"telecloud/utils"
)

type Config struct {
	APIID            int
	APIHash          string
	UploadThreads    int
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

	// Resumable chunked uploads (HTTP transport chunks, independent of the
	// Telegram storage parts they are assembled into).
	ChunkedUpload ChunkedUploadConfig
}

// Hard bounds for HTTP transport chunks. The upper bound keeps each request
// body safely below the 100 MB request limit enforced by Cloudflare's free and
// pro plans.
const (
	MinChunkedUploadChunkSize = 1 << 20  // 1 MiB
	MaxChunkedUploadChunkSize = 50 << 20 // 50 MiB
)

// ChunkedUploadConfig tunes the resumable upload-session API (/api/uploads).
type ChunkedUploadConfig struct {
	Enabled bool
	// Threshold is the file size above which clients should switch to the
	// session protocol. It is advertised via GET /api/uploads/config.
	Threshold int64
	// ChunkSize is the default size of one HTTP request body.
	ChunkSize int64
	// MaxParallel caps in-flight chunk requests per session; MaxInflight caps
	// them process-wide. Excess requests receive 429 with Retry-After.
	MaxParallel int
	MaxInflight int
	// SessionTTL is how long an unfinished session survives without activity.
	SessionTTL time.Duration
	// StagedParts bounds how many Telegram storage parts may be staged on local
	// disk per session at once (disk use <= StagedParts x part size).
	StagedParts int
	// PartSize optionally lowers the Telegram storage part size (0 = use the
	// account limit detected at login).
	PartSize int64
	// MaxRetries bounds Telegram upload attempts for one storage part.
	MaxRetries int
	// Dir holds staging files. It must not be inside TempDir, whose contents
	// are swept by the legacy cleaner.
	Dir string
}

// EffectivePartSize returns the Telegram storage part size for a session using
// chunkSize: the largest multiple of chunkSize that fits within the account
// limit (and the optional PartSize override), so a chunk never straddles two
// parts.
func (c *Config) EffectivePartSize(chunkSize int64) int64 {
	limit := c.MaxPartSize
	if p := c.ChunkedUpload.PartSize; p > 0 && (limit <= 0 || p < limit) {
		limit = p
	}
	if chunkSize <= 0 {
		return limit
	}
	if limit < chunkSize {
		return chunkSize
	}
	return (limit / chunkSize) * chunkSize
}

func loadChunkedUpload(tempDir string) ChunkedUploadConfig {
	mb := func(key string, def int64) int64 {
		v, err := strconv.ParseInt(strings.TrimSpace(getEnv(key, "")), 10, 64)
		if err != nil || v <= 0 {
			return def
		}
		return v
	}
	num := func(key string, def, max int) int {
		v, err := strconv.Atoi(strings.TrimSpace(getEnv(key, "")))
		if err != nil || v <= 0 {
			return def
		}
		if v > max {
			return max
		}
		return v
	}

	chunk := mb("CHUNKED_UPLOAD_CHUNK_SIZE_MB", 32) << 20
	if chunk > MaxChunkedUploadChunkSize {
		chunk = MaxChunkedUploadChunkSize
	}
	if chunk < MinChunkedUploadChunkSize {
		chunk = MinChunkedUploadChunkSize
	}

	dir := getEnv("CHUNKED_UPLOAD_DIR", "")
	if dir == "" {
		dir = filepath.Join(filepath.Dir(filepath.Clean(tempDir)), "upload_sessions")
	}

	return ChunkedUploadConfig{
		Enabled:     !strings.EqualFold(strings.TrimSpace(getEnv("CHUNKED_UPLOAD_ENABLED", "true")), "false"),
		Threshold:   mb("CHUNKED_UPLOAD_THRESHOLD_MB", 80) << 20,
		ChunkSize:   chunk,
		MaxParallel: num("CHUNKED_UPLOAD_MAX_PARALLEL", 4, 16),
		MaxInflight: num("CHUNKED_UPLOAD_MAX_INFLIGHT", 8, 64),
		SessionTTL:  time.Duration(num("CHUNKED_UPLOAD_SESSION_TTL_HOURS", 48, 24*365)) * time.Hour,
		StagedParts: num("CHUNKED_UPLOAD_STAGED_PARTS", 2, 8),
		PartSize:    int64(num("CHUNKED_UPLOAD_PART_MB", 0, 1<<20)) << 20,
		MaxRetries:  num("CHUNKED_UPLOAD_MAX_RETRIES", 6, 50),
		Dir:         dir,
	}
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

	tempDir := getEnv("TEMP_DIR", filepath.Join(os.TempDir(), "telecloud_temp_chunks"))

	return &Config{
		APIID:            apiID,
		APIHash:          apiHash,
		UploadThreads:    uploadThreads,
		DownloadPrefetch: downloadPrefetch,
		DatabaseDriver:   strings.ToLower(getEnv("DATABASE_DRIVER", "sqlite")),
		DatabasePath:     getEnv("DATABASE_PATH", "database.db"),
		DatabaseDSN:      getEnv("DATABASE_DSN", ""),
		ThumbsDir:        getEnv("THUMBS_DIR", "static/thumbs"),
		LogGroupID:       logGroupID,
		Port:             getEnv("PORT", "8091"),
		TempDir:          tempDir,
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
		ChunkedUpload:    loadChunkedUpload(tempDir),
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
