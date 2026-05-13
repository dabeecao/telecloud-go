package s3

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"telecloud/config"
	"telecloud/database"
	"telecloud/tgclient"
	"time"

	"github.com/google/uuid"
	"github.com/johannesboyne/gofakes3"
)

type TelecloudBackend struct {
	cfg      *config.Config
	username string
	isAdmin  bool
}

func NewBackend(cfg *config.Config, username string, isAdmin bool) *TelecloudBackend {
	return &TelecloudBackend{
		cfg:      cfg,
		username: username,
		isAdmin:  isAdmin,
	}
}

// mapPath maps an S3 key to a database path and filename.
func (b *TelecloudBackend) mapPath(s3Key string) (dbDir string, dbBase string) {
	cleanPath := path.Clean("/" + s3Key)
	var fullPath string

	if b.isAdmin {
		fullPath = cleanPath
	} else {
		if cleanPath == "/" {
			fullPath = "/" + b.username
		} else {
			fullPath = "/" + b.username + cleanPath
		}
	}

	if fullPath == "/" {
		return "/", ""
	}
	return path.Dir(fullPath), path.Base(fullPath)
}

func (b *TelecloudBackend) ListBuckets() ([]gofakes3.BucketInfo, error) {
	// Everyone sees a virtual bucket named "telecloud"
	return []gofakes3.BucketInfo{
		{
			Name:         "telecloud",
			CreationDate: gofakes3.NewContentTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		},
	}, nil
}

func (b *TelecloudBackend) ListBucket(name string, prefix *gofakes3.Prefix, page gofakes3.ListBucketPage) (*gofakes3.ObjectList, error) {
	if name != "telecloud" {
		return nil, gofakes3.BucketNotFound(name)
	}

	var files []database.File
	var err error

	s3Prefix := ""
	if prefix != nil {
		s3Prefix = prefix.Prefix
	}

	searchDir, searchBase := b.mapPath(s3Prefix)
	
	// If the prefix doesn't end in a slash, it might be a partial filename.
	// We need to search in the parent directory for files starting with searchBase.
	isPartialFile := s3Prefix != "" && !strings.HasSuffix(s3Prefix, "/")

	if prefix != nil && prefix.HasDelimiter && prefix.Delimiter == "/" {
		// Non-recursive: list children of exact directory
		fullSearchPath := searchDir
		if searchBase != "" && !isPartialFile {
			fullSearchPath = path.Join(searchDir, searchBase)
		}
		
		query := "SELECT id, path, filename, size, is_folder, created_at FROM files WHERE path = ? AND owner = ? AND deleted_at IS NULL AND (is_folder = 1 OR message_id IS NOT NULL)"
		args := []interface{}{fullSearchPath, b.username}
		
		if isPartialFile {
			query += " AND filename LIKE ?"
			args = append(args, searchBase+"%")
		}
		
		query += " ORDER BY filename ASC"
		err = database.RODB.Select(&files, query, args...)
	} else {
		// Recursive list: list everything starting with the prefix path
		fullSearchPath := searchDir
		if searchBase != "" && !isPartialFile {
			fullSearchPath = path.Join(searchDir, searchBase)
		}

		likePattern := fullSearchPath + "/%"
		if fullSearchPath == "/" {
			likePattern = "/%"
		}

		query := "SELECT id, path, filename, size, is_folder, created_at FROM files WHERE (path = ? OR path LIKE ?) AND owner = ? AND deleted_at IS NULL AND (is_folder = 1 OR message_id IS NOT NULL)"
		args := []interface{}{fullSearchPath, likePattern, b.username}

		if isPartialFile {
			query = "SELECT id, path, filename, size, is_folder, created_at FROM files WHERE ((path = ? AND filename LIKE ?) OR (path LIKE ?)) AND owner = ? AND deleted_at IS NULL AND (is_folder = 1 OR message_id IS NOT NULL)"
			args = []interface{}{searchDir, searchBase + "%", path.Join(searchDir, searchBase) + "/%", b.username}
		}

		query += " ORDER BY path ASC, filename ASC"
		if page.MaxKeys > 0 {
			query += fmt.Sprintf(" LIMIT %d", page.MaxKeys+1)
		}
		err = database.RODB.Select(&files, query, args...)
	}

	if err != nil {
		return nil, err
	}

	objects := gofakes3.NewObjectList()

	dbPrefix := "/" + b.username + "/"
	if b.isAdmin {
		dbPrefix = "/"
	}

	count := 0
	for _, f := range files {
		fullPath := path.Join(f.Path, f.Filename)

		key := strings.TrimPrefix(fullPath, dbPrefix)
		if key == fullPath && !b.isAdmin {
			continue
		}

		if prefix != nil && !prefix.Match(key, nil) {
			continue
		}

		// Handle pagination (basic)
		if page.MaxKeys > 0 && int64(count) >= page.MaxKeys {
			objects.IsTruncated = true
			break
		}

		if f.IsFolder {
			objects.AddPrefix(key + "/")
		} else {
			objects.Add(&gofakes3.Content{
				Key:          key,
				LastModified: gofakes3.NewContentTime(f.CreatedAt),
				Size:         f.Size,
				ETag:         fmt.Sprintf("\"%x\"", f.ID),
				StorageClass: gofakes3.StorageStandard,
			})
		}
		count++
	}

	return objects, nil
}

func (b *TelecloudBackend) GetObject(bucketName, objectName string, rangeRequest *gofakes3.ObjectRangeRequest) (*gofakes3.Object, error) {
	if bucketName != "telecloud" {
		return nil, gofakes3.BucketNotFound(bucketName)
	}

	dbPath, filename := b.mapPath(objectName)

	var file database.File
	query := "SELECT * FROM files WHERE path = ? AND filename = ? AND owner = ? AND deleted_at IS NULL"
	args := []interface{}{dbPath, filename, b.username}

	err := database.RODB.Get(&file, query, args...)
	if err != nil {
		return nil, gofakes3.KeyNotFound(objectName)
	}

	rs, err := tgclient.GetTelegramFileReader(context.Background(), file, b.cfg)
	if err != nil {
		return nil, err
	}

	mimeType := "application/octet-stream"
	if file.MimeType != nil && *file.MimeType != "" {
		mimeType = *file.MimeType
	}
	metadata := make(map[string]string)
	metadata["Content-Type"] = mimeType

	var body io.ReadCloser
	size := file.Size

	if rangeRequest != nil {
		contentRange, err := rangeRequest.Range(size)
		if err != nil {
			rs.Close()
			return nil, err
		}
		_, err = rs.Seek(contentRange.Start, io.SeekStart)
		if err != nil {
			rs.Close()
			return nil, err
		}
		body = &closerReader{r: io.LimitReader(rs, contentRange.Length), c: rs}
		size = contentRange.Length
	} else {
		body = rs
	}

	return &gofakes3.Object{
		Name:     objectName,
		Metadata: metadata,
		Size:     size,
		Contents: body,
	}, nil
}

func (b *TelecloudBackend) HeadObject(bucketName, objectName string) (*gofakes3.Object, error) {
	if bucketName != "telecloud" {
		return nil, gofakes3.BucketNotFound(bucketName)
	}

	dbPath, filename := b.mapPath(objectName)

	var file database.File
	query := "SELECT id, filename, size, is_folder, created_at, mime_type FROM files WHERE path = ? AND filename = ? AND owner = ? AND deleted_at IS NULL"
	args := []interface{}{dbPath, filename, b.username}

	err := database.RODB.Get(&file, query, args...)
	if err != nil {
		return nil, gofakes3.KeyNotFound(objectName)
	}

	mimeType := "application/octet-stream"
	if file.MimeType != nil && *file.MimeType != "" {
		mimeType = *file.MimeType
	}
	metadata := make(map[string]string)
	metadata["Content-Type"] = mimeType

	return &gofakes3.Object{
		Name:     objectName,
		Metadata: metadata,
		Size:     file.Size,
		Contents: nil, // Optimization: HeadObject doesn't need to read content
	}, nil
}

func (b *TelecloudBackend) DeleteObject(bucketName, objectName string) (gofakes3.ObjectDeleteResult, error) {
	if bucketName != "telecloud" {
		return gofakes3.ObjectDeleteResult{}, gofakes3.BucketNotFound(bucketName)
	}

	dbPath, filename := b.mapPath(objectName)

	var file database.File
	query := "SELECT id FROM files WHERE path = ? AND filename = ? AND owner = ? AND deleted_at IS NULL"
	args := []interface{}{dbPath, filename, b.username}

	err := database.RODB.Get(&file, query, args...)
	if err != nil {
		return gofakes3.ObjectDeleteResult{}, nil
	}

	now := time.Now()
	database.DB.Exec("UPDATE files SET deleted_at = ? WHERE id = ?", now, file.ID)
	return gofakes3.ObjectDeleteResult{IsDeleteMarker: false}, nil
}

func (b *TelecloudBackend) DeleteMulti(bucketName string, objects ...string) (gofakes3.MultiDeleteResult, error) {
	var res gofakes3.MultiDeleteResult
	for _, obj := range objects {
		_, err := b.DeleteObject(bucketName, obj)
		if err != nil {
			res.Error = append(res.Error, gofakes3.ErrorResult{Code: "InternalError", Message: err.Error(), Key: obj})
		} else {
			res.Deleted = append(res.Deleted, gofakes3.ObjectID{Key: obj})
		}
	}
	return res, nil
}

func (b *TelecloudBackend) PutObject(bucketName, key string, meta map[string]string, input io.Reader, size int64, conditions *gofakes3.PutConditions) (gofakes3.PutObjectResult, error) {
	if bucketName != "telecloud" {
		return gofakes3.PutObjectResult{}, gofakes3.BucketNotFound(bucketName)
	}

	dbPath, filename := b.mapPath(key)
	fullPath := path.Join(dbPath, filename)

	// Handle Folder creation (S3 folders end with /)
	if strings.HasSuffix(key, "/") {
		err := database.EnsureFoldersExist(fullPath, b.username)
		if err != nil {
			return gofakes3.PutObjectResult{}, err
		}
		// Also insert the folder entry itself if it doesn't exist
		parentPath := path.Dir(fullPath)
		folderName := path.Base(fullPath)
		_, err = database.DB.Exec(
			database.InsertIgnoreSQL("files", "filename, path, is_folder, owner", "?, ?, 1, ?"),
			folderName, parentPath, b.username,
		)
		return gofakes3.PutObjectResult{}, err
	}

	taskID := uuid.New().String()
	os.MkdirAll(b.cfg.TempDir, os.ModePerm)
	tempFilePath := filepath.Join(b.cfg.TempDir, taskID+"_"+filename)

	out, err := os.OpenFile(tempFilePath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return gofakes3.PutObjectResult{}, err
	}
	_, err = io.Copy(out, input)
	out.Sync()
	out.Close()
	defer os.Remove(tempFilePath)

	if err != nil {
		return gofakes3.PutObjectResult{}, fmt.Errorf("failed to write temp file: %w", err)
	}

	mimeType := meta["Content-Type"]
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	_, _, err = tgclient.ProcessCompleteUploadSync(context.Background(), tempFilePath, filename, dbPath, mimeType, taskID, b.cfg, true, b.username)
	if err != nil {
		return gofakes3.PutObjectResult{}, err
	}

	return gofakes3.PutObjectResult{}, nil
}

func (b *TelecloudBackend) CopyObject(srcBucket, srcKey, dstBucket, dstKey string, meta map[string]string) (gofakes3.CopyObjectResult, error) {
	if srcBucket != "telecloud" || dstBucket != "telecloud" {
		return gofakes3.CopyObjectResult{}, gofakes3.ErrNotImplemented
	}

	srcDbPath, srcFilename := b.mapPath(srcKey)
	dstDbPath, dstFilename := b.mapPath(dstKey)

	var file database.File
	query := "SELECT id, message_id, filename, path, size, mime_type, is_folder, thumb_path FROM files WHERE path = ? AND filename = ? AND owner = ? AND deleted_at IS NULL"
	args := []interface{}{srcDbPath, srcFilename, b.username}

	err := database.RODB.Get(&file, query, args...)
	if err != nil {
		return gofakes3.CopyObjectResult{}, gofakes3.KeyNotFound(srcKey)
	}

	// Ensure destination directory exists
	database.EnsureFoldersExist(dstDbPath, b.username)

	tx, err := database.DB.Beginx()
	if err != nil {
		return gofakes3.CopyObjectResult{}, err
	}
	defer tx.Rollback()

	// Perform the copy in database
	res, err := tx.Exec(
		"INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path, owner) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		file.MessageID, dstFilename, dstDbPath, file.Size, file.MimeType, file.IsFolder, file.ThumbPath, b.username,
	)
	if err != nil {
		return gofakes3.CopyObjectResult{}, err
	}

	newFileID, _ := res.LastInsertId()
	if !file.IsFolder {
		_, err = tx.Exec("INSERT INTO file_parts (file_id, part_index, message_id, size) SELECT ?, part_index, message_id, size FROM file_parts WHERE file_id = ?", newFileID, file.ID)
		if err != nil {
			return gofakes3.CopyObjectResult{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return gofakes3.CopyObjectResult{}, err
	}

	return gofakes3.CopyObjectResult{
		ETag:         fmt.Sprintf("\"%x\"", newFileID),
		LastModified: gofakes3.NewContentTime(time.Now()),
	}, nil
}

func (b *TelecloudBackend) CreateBucket(name string) error {
	if name == "telecloud" {
		return nil
	}
	return gofakes3.ErrNotImplemented
}

func (b *TelecloudBackend) DeleteBucket(name string) error {
	return gofakes3.ErrNotImplemented
}

func (b *TelecloudBackend) BucketExists(name string) (bool, error) {
	return name == "telecloud", nil
}

func (b *TelecloudBackend) ForceDeleteBucket(name string) error {
	return gofakes3.ErrNotImplemented
}
type closerReader struct {
	r io.Reader
	c io.Closer
}

func (cr *closerReader) Read(p []byte) (int, error) {
	return cr.r.Read(p)
}

func (cr *closerReader) Close() error {
	return cr.c.Close()
}
