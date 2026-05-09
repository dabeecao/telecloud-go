package webdav

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"telecloud/config"
	"telecloud/database"
	"telecloud/tgclient"
	"mime"

	"github.com/google/uuid"
)

type telecloudFileInfo struct {
	name  string
	size  int64
	isDir bool
	mtime time.Time
}

func (fi *telecloudFileInfo) Name() string       { return fi.name }
func (fi *telecloudFileInfo) Size() int64        { return fi.size }
func (fi *telecloudFileInfo) Mode() os.FileMode  { if fi.isDir { return os.ModeDir | 0755 } else { return 0644 } }
func (fi *telecloudFileInfo) ModTime() time.Time { return fi.mtime }
func (fi *telecloudFileInfo) IsDir() bool        { return fi.isDir }
func (fi *telecloudFileInfo) Sys() interface{}   { return nil }

type telecloudFile struct {
	isDir bool
	path  string
	name  string
	size  int64
	mtime time.Time
	rs    io.ReadSeekCloser

	dirItems []os.FileInfo
	dirIndex int
	isAdmin  bool
	username string
	fs       *telecloudFS
}

func (f *telecloudFile) Read(p []byte) (int, error) {
	if f.isDir {
		return 0, io.ErrUnexpectedEOF
	}
	if f.rs == nil {
		return 0, io.EOF
	}
	return f.rs.Read(p)
}

func (f *telecloudFile) Seek(offset int64, whence int) (int64, error) {
	if f.isDir {
		return 0, io.ErrUnexpectedEOF
	}
	if f.rs == nil {
		return 0, io.EOF
	}
	return f.rs.Seek(offset, whence)
}

func (f *telecloudFile) Readdir(count int) ([]os.FileInfo, error) {
	if !f.isDir {
		return nil, io.ErrUnexpectedEOF
	}
	
	if f.dirItems == nil {
		searchPath := f.path
		cacheKey := f.username + ":" + searchPath

		// Check cache
		if f.fs != nil {
			if val, ok := f.fs.dirCache.Load(cacheKey); ok {
				entry := val.(*dirCacheEntry)
				if time.Now().Before(entry.expiresAt) {
					f.dirItems = entry.items
				}
			}
		}

		if f.dirItems == nil {
			var files []database.File
			// Optimized query: message_id IS NOT NULL is sufficient for visibility as it's set as soon as the first part is uploaded.
			// This avoids the expensive subquery on file_parts.
			err := database.RODB.Select(&files, "SELECT filename, size, is_folder, created_at FROM files WHERE path = ? AND owner = ? AND (is_folder = 1 OR message_id IS NOT NULL) ORDER BY is_folder DESC, filename ASC", searchPath, f.username)
			if err != nil {
				return nil, err
			}

			f.dirItems = make([]os.FileInfo, 0, len(files))
			for _, v := range files {
				f.dirItems = append(f.dirItems, &telecloudFileInfo{
					name:  v.Filename,
					size:  v.Size,
					isDir: v.IsFolder,
					mtime: v.CreatedAt,
				})
			}

			// Update cache (10 seconds TTL)
			if f.fs != nil {
				f.fs.dirCache.Store(cacheKey, &dirCacheEntry{
					items:     f.dirItems,
					expiresAt: time.Now().Add(10 * time.Second),
				})
			}
		}
	}

	if count <= 0 {
		return f.dirItems, nil
	}

	if f.dirIndex >= len(f.dirItems) {
		return nil, io.EOF
	}

	end := f.dirIndex + count
	if end > len(f.dirItems) {
		end = len(f.dirItems)
	}

	items := f.dirItems[f.dirIndex:end]
	f.dirIndex = end
	return items, nil
}

func (f *telecloudFile) Stat() (os.FileInfo, error) {
	return &telecloudFileInfo{
		name:  f.name,
		size:  f.size,
		isDir: f.isDir,
		mtime: f.mtime,
	}, nil
}

func (f *telecloudFile) Close() error {
	if f.rs != nil {
		return f.rs.Close()
	}
	return nil
}

func (f *telecloudFile) Write(p []byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}


// fileWriter is used for uploads
type fileWriter struct {
	ctx      context.Context
	cfg      *config.Config
	dir      string
	filename string
	tempPath string
	file     *os.File
	taskID    string
	overwrite bool
	owner     string
}

func newFileWriter(ctx context.Context, cfg *config.Config, dir, filename string, overwrite bool, owner string) *fileWriter {
	taskID := uuid.New().String()
	tempDir := filepath.Join(cfg.TempDir, "webdav")
	os.MkdirAll(tempDir, os.ModePerm)
	tempPath := filepath.Join(tempDir, taskID+"_"+filepath.Base(filename))

	f, _ := os.OpenFile(tempPath, os.O_CREATE|os.O_RDWR, 0644)

	return &fileWriter{
		ctx:       ctx,
		cfg:       cfg,
		dir:       dir,
		filename:  filename,
		tempPath:  tempPath,
		file:      f,
		taskID:    taskID,
		overwrite: overwrite,
		owner:     owner,
	}
}

func (w *fileWriter) Write(p []byte) (int, error) {
	if w.file == nil {
		return 0, io.ErrClosedPipe
	}
	return w.file.Write(p)
}

func (w *fileWriter) Close() error {
	if w.file != nil {
		w.file.Close()
		w.file = nil

		// Push to Telegram in background
		go func() {
			// Multi-part upload allows any size

			// Detect MIME type from extension
			mimeType := mime.TypeByExtension(filepath.Ext(w.filename))
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}

			tgclient.ProcessCompleteUpload(context.Background(), w.tempPath, w.filename, w.dir, mimeType, w.taskID, w.cfg, w.overwrite, w.owner)
			os.Remove(w.tempPath)
		}()
	}
	return nil
}

func (w *fileWriter) Read(p []byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func (w *fileWriter) Seek(offset int64, whence int) (int64, error) {
	if w.file == nil {
		return 0, io.ErrClosedPipe
	}
	return w.file.Seek(offset, whence)
}

func (w *fileWriter) Readdir(count int) ([]os.FileInfo, error) {
	return nil, io.ErrUnexpectedEOF
}

func (w *fileWriter) Stat() (os.FileInfo, error) {
	if w.file == nil {
		return nil, io.ErrClosedPipe
	}
	stat, err := os.Stat(w.tempPath)
	if err != nil {
		return nil, err
	}
	return &telecloudFileInfo{
		name:  w.filename,
		size:  stat.Size(),
		isDir: false,
		mtime: stat.ModTime(),
	}, nil
}
