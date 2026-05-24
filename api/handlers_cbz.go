package api

import (
	"archive/zip"
	"encoding/binary"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"telecloud/database"
	"telecloud/tgclient"
	"telecloud/utils"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
)

type zipMetadataCacheEntry struct {
	cdOffset     int64
	fileSize     int64
	cacheData    []byte
	lastAccessed time.Time
}

var (
	zipMetadataCache        = make(map[int]*zipMetadataCacheEntry)
	zipMetadataCacheMutex   sync.Mutex
	zipMetadataSingleflight singleflight.Group
)

func init() {
	// Clean up expired cache entries every 5 minutes
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			now := time.Now()
			zipMetadataCacheMutex.Lock()
			for id, entry := range zipMetadataCache {
				if now.Sub(entry.lastAccessed) > 15*time.Minute {
					delete(zipMetadataCache, id)
				}
			}
			zipMetadataCacheMutex.Unlock()
		}
	}()
}

type readerAt struct {
	rs       io.ReadSeekCloser
	mu       sync.Mutex
	fileID   int
	fileSize int64
}

func (r *readerAt) ReadAt(p []byte, off int64) (n int, err error) {
	// Check if the read range falls within cached Central Directory bytes
	if r.fileID > 0 && r.fileSize > 0 {
		zipMetadataCacheMutex.Lock()
		entry, cached := zipMetadataCache[r.fileID]
		if cached {
			entry.lastAccessed = time.Now()
			zipMetadataCacheMutex.Unlock()

			// If the requested read is entirely inside the cached range [cdOffset, fileSize)
			if off >= entry.cdOffset && off+int64(len(p)) <= r.fileSize {
				localOff := off - entry.cdOffset
				n = copy(p, entry.cacheData[localOff:])
				return n, nil
			}
		} else {
			zipMetadataCacheMutex.Unlock()
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	_, err = r.rs.Seek(off, io.SeekStart)
	if err != nil {
		return 0, err
	}
	return io.ReadFull(r.rs, p)
}

func ensureZipMetadataCached(fileID int, fileSize int64, rs io.ReaderAt) {
	if fileID <= 0 || fileSize < 1024 {
		return
	}

	zipMetadataCacheMutex.Lock()
	_, cached := zipMetadataCache[fileID]
	zipMetadataCacheMutex.Unlock()
	if cached {
		return
	}

	key := strconv.Itoa(fileID)
	_, _, _ = zipMetadataSingleflight.Do(key, func() (interface{}, error) {
		zipMetadataCacheMutex.Lock()
		_, cached := zipMetadataCache[fileID]
		zipMetadataCacheMutex.Unlock()
		if cached {
			return nil, nil
		}

		// Read the last 1024 bytes to find EOCD
		readSize := int64(1024)
		if readSize > fileSize {
			readSize = fileSize
		}
		buf := make([]byte, readSize)
		n, err := rs.ReadAt(buf, fileSize-readSize)
		if err != nil && err != io.EOF {
			return nil, nil
		}
		buf = buf[:n]

		// Search backwards for standard EOCD signature: 0x06054b50 (\x50\x4b\x05\x06)
		var cdOffset, cdSize int64
		found := false
		for i := len(buf) - 22; i >= 0; i-- {
			if buf[i] == 0x50 && buf[i+1] == 0x4b && buf[i+2] == 0x05 && buf[i+3] == 0x06 {
				cdSize = int64(binary.LittleEndian.Uint32(buf[i+12 : i+16]))
				cdOffset = int64(binary.LittleEndian.Uint32(buf[i+16 : i+20]))
				found = true
				break
			}
		}

		if !found {
			return nil, nil
		}

		// Sanity checks
		if cdOffset < 0 || cdSize < 0 || cdOffset+cdSize > fileSize {
			return nil, nil
		}

		// Limit cache size to 10MB to avoid excessive memory consumption
		cacheSize := fileSize - cdOffset
		if cacheSize > 10*1024*1024 {
			return nil, nil
		}

		// Fetch the entire Central Directory + EOCD range
		cacheData := make([]byte, cacheSize)
		_, err = rs.ReadAt(cacheData, cdOffset)
		if err != nil {
			return nil, nil
		}

		// Cache it
		zipMetadataCacheMutex.Lock()
		zipMetadataCache[fileID] = &zipMetadataCacheEntry{
			cdOffset:     cdOffset,
			fileSize:     fileSize,
			cacheData:    cacheData,
			lastAccessed: time.Now(),
		}
		zipMetadataCacheMutex.Unlock()
		return nil, nil
	})
}

func (h *Handler) resolveComicFile(c *gin.Context, isShare bool) (database.File, error) {
	var item database.File
	if !isShare {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			return item, fmt.Errorf("invalid_id")
		}
		username := c.GetString("username")
		err = database.RODB.Get(&item, "SELECT * FROM files WHERE id = ? AND owner = ? AND deleted_at IS NULL", id, username)
		if err != nil {
			return item, fmt.Errorf("not_found")
		}
		return item, nil
	}

	// Shared path
	token := c.Param("token")
	idStr := c.Param("id")
	if idStr != "" {
		id, err := strconv.Atoi(idStr)
		if err != nil {
			return item, fmt.Errorf("invalid_id")
		}
		return h.resolveSharedFileInFolder(c, token, id)
	}

	// Single shared file (without id in the path)
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE share_token = ? AND deleted_at IS NULL", token); err != nil || item.IsFolder {
		return item, fmt.Errorf("not_found")
	}
	if !h.checkShareAuth(c, item) {
		return item, fmt.Errorf("unauthorized")
	}
	return item, nil
}

func (h *Handler) handleGetComicPages(c *gin.Context) {
	h.serveComicPages(c, false)
}

func (h *Handler) handleGetSharedComicPages(c *gin.Context) {
	h.serveComicPages(c, true)
}

func (h *Handler) serveComicPages(c *gin.Context, isShare bool) {
	item, err := h.resolveComicFile(c, isShare)
	if err != nil {
		if err.Error() == "unauthorized" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "password_required"})
		} else if err.Error() == "forbidden" {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		} else {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		}
		return
	}

	if isShare && item.Size > 150*1024*1024 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file_too_large"})
		return
	}

	// We only process .cbz extension
	ext := strings.ToLower(filepath.Ext(item.Filename))
	if ext != ".cbz" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_format"})
		return
	}

	// Get reader from Telegram
	reader, err := tgclient.GetTelegramFileReader(c.Request.Context(), item, h.cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_open_telegram_stream"})
		return
	}
	defer reader.Close()

	// Wrap in ReaderAt
	rAt := &readerAt{rs: reader, fileID: item.ID, fileSize: item.Size}

	// Ensure Central Directory is cached
	ensureZipMetadataCached(item.ID, item.Size, rAt)

	// Read ZIP
	zipReader, err := zip.NewReader(rAt, item.Size)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_parse_zip_archive"})
		return
	}

	// Filter and collect image files
	var pages []string
	for _, f := range zipReader.File {
		// Ignore directories and system files
		if f.FileInfo().IsDir() {
			continue
		}
		name := strings.ToLower(f.Name)
		if strings.HasPrefix(filepath.Base(name), ".") || strings.Contains(name, "__macosx") {
			continue
		}
		// Match image extensions
		if strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") ||
			strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".webp") ||
			strings.HasSuffix(name, ".gif") || strings.HasSuffix(name, ".bmp") {
			pages = append(pages, f.Name)
		}
	}

	// Sort pages naturally
	sort.Slice(pages, func(i, j int) bool {
		return utils.NaturalLess(pages[i], pages[j])
	})

	if len(pages) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not_a_comic"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"pages": pages})
}

func (h *Handler) handleGetComicPage(c *gin.Context) {
	h.serveComicPage(c, false)
}

func (h *Handler) handleGetSharedComicPage(c *gin.Context) {
	h.serveComicPage(c, true)
}

func (h *Handler) serveComicPage(c *gin.Context, isShare bool) {
	item, err := h.resolveComicFile(c, isShare)
	if err != nil {
		if err.Error() == "unauthorized" {
			c.AbortWithStatus(http.StatusUnauthorized)
		} else if err.Error() == "forbidden" {
			c.AbortWithStatus(http.StatusForbidden)
		} else {
			c.AbortWithStatus(http.StatusNotFound)
		}
		return
	}

	if isShare && item.Size > 150*1024*1024 {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	pagePath := c.Query("path")
	if pagePath == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// We only process .cbz extension
	ext := strings.ToLower(filepath.Ext(item.Filename))
	if ext != ".cbz" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Check resource cache first to avoid Telegram reader overhead
	if data, cachedMime, found := getCachedZipResource(item.ID, pagePath); found {
		c.Header("Content-Type", cachedMime)
		c.Header("Cache-Control", "private, max-age=86400")
		c.Writer.Write(data)
		return
	}

	// Get reader from Telegram
	reader, err := tgclient.GetTelegramFileReader(c.Request.Context(), item, h.cfg)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	defer reader.Close()

	// Wrap in ReaderAt
	rAt := &readerAt{rs: reader, fileID: item.ID, fileSize: item.Size}

	// Ensure Central Directory is cached
	ensureZipMetadataCached(item.ID, item.Size, rAt)

	// Read ZIP
	zipReader, err := zip.NewReader(rAt, item.Size)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	// Find the requested file inside the zip
	var targetFile *zip.File
	for _, f := range zipReader.File {
		if f.Name == pagePath {
			targetFile = f
			break
		}
	}

	if targetFile == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	// Open the image inside the zip
	fileReader, err := targetFile.Open()
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	defer fileReader.Close()

	// Detect MIME type
	mimeType := mime.TypeByExtension(filepath.Ext(targetFile.Name))
	if mimeType == "" {
		mimeType = "image/jpeg" // Safe fallback
	}

	c.Header("Content-Type", mimeType)
	c.Header("Cache-Control", "private, max-age=86400") // 24 hours browser cache

	// Read and cache if resource size fits criteria (<2MB)
	if targetFile.UncompressedSize64 <= 2*1024*1024 {
		data, err := io.ReadAll(fileReader)
		if err == nil {
			setCachedZipResource(item.ID, targetFile.Name, data, mimeType)
			c.Writer.Write(data)
			return
		}
	}

	// Stream file to response
	io.Copy(c.Writer, fileReader)
}
