package api

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

type zipResourceCacheEntry struct {
	data         []byte
	mimeType     string
	lastAccessed time.Time
}

var (
	zipResourceCache      = make(map[string]*zipResourceCacheEntry)
	zipResourceCacheMutex sync.Mutex
	maxTotalCacheSize     = int64(40 * 1024 * 1024) // 40 MB max total cache size in memory
	currentCacheSize      = int64(0)
)

func init() {
	// Clean up expired entries every 2 minutes (inactive for 10 minutes)
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		for range ticker.C {
			now := time.Now()
			zipResourceCacheMutex.Lock()
			for key, entry := range zipResourceCache {
				if now.Sub(entry.lastAccessed) > 10*time.Minute {
					currentCacheSize -= int64(len(entry.data))
					delete(zipResourceCache, key)
				}
			}
			zipResourceCacheMutex.Unlock()
		}
	}()
}

func getCachedZipResource(fileID int, path string) ([]byte, string, bool) {
	key := getCacheKey(fileID, path)
	zipResourceCacheMutex.Lock()
	defer zipResourceCacheMutex.Unlock()

	entry, found := zipResourceCache[key]
	if !found {
		return nil, "", false
	}
	entry.lastAccessed = time.Now()
	return entry.data, entry.mimeType, true
}

func setCachedZipResource(fileID int, path string, data []byte, mimeType string) {
	// Only cache files smaller than 2MB to prevent memory exhaustion
	if len(data) > 2*1024*1024 {
		return
	}

	key := getCacheKey(fileID, path)
	zipResourceCacheMutex.Lock()
	defer zipResourceCacheMutex.Unlock()

	// If entry exists, update size
	if old, found := zipResourceCache[key]; found {
		currentCacheSize -= int64(len(old.data))
	}

	// Evict entries if total size exceeds limit
	dataSize := int64(len(data))
	if currentCacheSize+dataSize > maxTotalCacheSize {
		// Evict oldest entries until we have space
		for currentCacheSize+dataSize > maxTotalCacheSize && len(zipResourceCache) > 0 {
			var oldestKey string
			var oldestTime time.Time
			first := true
			for k, v := range zipResourceCache {
				if first || v.lastAccessed.Before(oldestTime) {
					oldestKey = k
					oldestTime = v.lastAccessed
					first = false
				}
			}
			if oldestKey != "" {
				currentCacheSize -= int64(len(zipResourceCache[oldestKey].data))
				delete(zipResourceCache, oldestKey)
			} else {
				break
			}
		}
	}

	// Double check we have space or it fits
	if currentCacheSize+dataSize <= maxTotalCacheSize {
		zipResourceCache[key] = &zipResourceCacheEntry{
			data:         data,
			mimeType:     mimeType,
			lastAccessed: time.Now(),
		}
		currentCacheSize += dataSize
	}
}

func getCacheKey(fileID int, path string) string {
	return strconv.Itoa(fileID) + ":" + strings.ToLower(path)
}
