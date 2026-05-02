package config

import (
	"log"
	"os"
	"path/filepath"
	"strconv"

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
	WebAuthnRPID     string
	WebAuthnRPOrigin   string
	MaxPartSize      int64
}

func Load() *Config {
	err := godotenv.Load()
	if err != nil && !os.IsNotExist(err) {
		log.Printf("Error loading .env file: %v", err)
	}

	apiID, _ := strconv.Atoi(os.Getenv("API_ID"))
	apiHash := os.Getenv("API_HASH")

	if apiID == 0 || apiHash == "" {
		log.Fatal("Error: API_ID and API_HASH must be set in .env. Please get them from https://my.telegram.org")
	}

	uploadThreads, _ := strconv.Atoi(getEnv("TG_UPLOAD_THREADS", "2"))
	if uploadThreads <= 0 {
		uploadThreads = 2
	}

	logGroupID := os.Getenv("LOG_GROUP_ID")

	// MaxPartSize will be auto-detected in tgclient based on account status (Premium/Regular)
	maxPartSizeMB := int64(1900) 

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
		FFMPEGPath:      getEnv("FFMPEG_PATH", "ffmpeg"),
		WebAuthnRPID:     getEnv("WEBAUTHN_RPID", "localhost"),
		WebAuthnRPOrigin:   getEnv("WEBAUTHN_RPORIGIN", "http://localhost:8091"),
		MaxPartSize:      maxPartSizeMB * 1024 * 1024,
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
