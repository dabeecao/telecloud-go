package config

import (
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	APIID           int
	APIHash         string
	MaxUploadSizeMB int
	DatabasePath    string
	ThumbsDir       string
	LogGroupID      string
	Port            string
	TempDir         string
	ProxyURL        string
	Version         string
	SessionFile     string
	FFMPEGPath      string
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

	maxUploadSizeMB, _ := strconv.Atoi(getEnv("MAX_UPLOAD_SIZE_MB", "0"))

	logGroupID := os.Getenv("LOG_GROUP_ID")

	return &Config{
		APIID:           apiID,
		APIHash:         apiHash,
		MaxUploadSizeMB: maxUploadSizeMB,
		DatabasePath:    getEnv("DATABASE_PATH", "database.db"),
		ThumbsDir:       getEnv("THUMBS_DIR", "static/thumbs"),
		LogGroupID:      logGroupID,
		Port:            getEnv("PORT", "8091"),
		TempDir:         getEnv("TEMP_DIR", os.TempDir()+"/telecloud_temp_chunks"),
		ProxyURL:        getEnv("PROXY_URL", ""),
		SessionFile:     getEnv("SESSION_FILE", "session.json"),
		FFMPEGPath:      getEnv("FFMPEG_PATH", "ffmpeg"),
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
