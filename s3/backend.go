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

// isChildAccountPath checks if a DB path belongs to a child account folder.
func isChildAccountPath(dbPath string) bool {
	dbPath = path.Clean(dbPath)
	if dbPath == "/" || dbPath == "." {
		return false
	}
	
	parts := strings.Split(strings.TrimPrefix(dbPath, "/"), "/")
	rootFolder := parts[0]

	var exists int
	database.DB.Get(&exists, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", rootFolder)
	return exists > 0
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
	fullSearchPath := searchDir
	if searchBase != "" {
		fullSearchPath = path.Join(searchDir, searchBase)
	}

	// Optimization: If we have a delimiter and prefix, we can narrow down the search
	if prefix != nil && prefix.HasDelimiter && prefix.Delimiter == "/" {
		if b.isAdmin {
			err = database.DB.Select(&files, "SELECT * FROM files WHERE path = ? ORDER BY filename ASC", fullSearchPath)
		} else {
			err = database.DB.Select(&files, "SELECT * FROM files WHERE path = ? AND owner = ? ORDER BY filename ASC", fullSearchPath, b.username)
		}
	} else {
		query := "SELECT * FROM files WHERE (path = ? OR path LIKE ?) AND owner = ? ORDER BY path ASC, filename ASC"
		args := []interface{}{fullSearchPath, fullSearchPath + "/%", b.username}
		if b.isAdmin {
			query = "SELECT * FROM files WHERE (path = ? OR path LIKE ?) ORDER BY path ASC, filename ASC"
			args = []interface{}{fullSearchPath, fullSearchPath + "/%"}
		}
		err = database.DB.Select(&files, query, args...)
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
		
		// Strict Admin isolation
		if b.isAdmin && isChildAccountPath(fullPath) {
			continue
		}

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
	fullPath := path.Join(dbPath, filename)

	if b.isAdmin && isChildAccountPath(fullPath) {
		return nil, gofakes3.KeyNotFound(objectName)
	}

	var file database.File
	query := "SELECT * FROM files WHERE path = ? AND filename = ? AND owner = ?"
	args := []interface{}{dbPath, filename, b.username}
	if b.isAdmin {
		query = "SELECT * FROM files WHERE path = ? AND filename = ?"
		args = []interface{}{dbPath, filename}
	}
	
	err := database.DB.Get(&file, query, args...)
	if err != nil {
		return nil, gofakes3.KeyNotFound(objectName)
	}

	rs, err := tgclient.GetTelegramFileReader(context.Background(), file, b.cfg)
	if err != nil {
		return nil, err
	}

	metadata := make(map[string]string)
	metadata["Content-Type"] = "application/octet-stream"

	var body io.ReadCloser
	size := file.Size

	if rangeRequest != nil {
		contentRange, err := rangeRequest.Range(size)
		if err != nil {
			return nil, err
		}
		_, err = rs.Seek(contentRange.Start, io.SeekStart)
		if err != nil {
			return nil, err
		}
		body = io.NopCloser(io.LimitReader(rs, contentRange.Length))
		size = contentRange.Length
	} else {
		body = io.NopCloser(rs)
	}

	return &gofakes3.Object{
		Name:     objectName,
		Metadata: metadata,
		Size:     size,
		Contents: body,
	}, nil
}

func (b *TelecloudBackend) HeadObject(bucketName, objectName string) (*gofakes3.Object, error) {
	return b.GetObject(bucketName, objectName, nil)
}

func (b *TelecloudBackend) DeleteObject(bucketName, objectName string) (gofakes3.ObjectDeleteResult, error) {
	if bucketName != "telecloud" {
		return gofakes3.ObjectDeleteResult{}, gofakes3.BucketNotFound(bucketName)
	}

	dbPath, filename := b.mapPath(objectName)
	fullPath := path.Join(dbPath, filename)

	if b.isAdmin && isChildAccountPath(fullPath) {
		return gofakes3.ObjectDeleteResult{}, nil
	}

	var file database.File
	query := "SELECT * FROM files WHERE path = ? AND filename = ? AND owner = ?"
	args := []interface{}{dbPath, filename, b.username}
	if b.isAdmin {
		query = "SELECT * FROM files WHERE path = ? AND filename = ?"
		args = []interface{}{dbPath, filename}
	}

	err := database.DB.Get(&file, query, args...)
	if err != nil {
		return gofakes3.ObjectDeleteResult{}, nil
	}

	if !file.IsFolder {
		// Collect all message IDs for this file (parts)
		var msgIDs []int
		database.DB.Select(&msgIDs, "SELECT message_id FROM file_parts WHERE file_id = ?", file.ID)
		if file.MessageID != nil {
			msgIDs = append(msgIDs, *file.MessageID)
		}

		if len(msgIDs) > 0 {
			go tgclient.DeleteMessages(context.Background(), b.cfg, msgIDs)
		}

		if file.ThumbPath != nil {
			var count int
			database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE thumb_path = ?", *file.ThumbPath)
			if count <= 1 {
				os.Remove(*file.ThumbPath)
			}
		}
	} else {
		// If it's a folder, we might want to prevent deleting non-empty folders via S3 DeleteObject 
		// but S3 actually allows deleting "folder objects" separately.
	}

	database.DB.Exec("DELETE FROM files WHERE id = ?", file.ID)
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

	// Strict Admin isolation
	if b.isAdmin && isChildAccountPath(fullPath) {
		return gofakes3.PutObjectResult{}, gofakes3.ErrNotImplemented // Or another appropriate error
	}

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
			"INSERT OR IGNORE INTO files (filename, path, is_folder, owner) VALUES (?, ?, 1, ?)",
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

	mimeType := meta["Content-Type"]
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	_, _, err = tgclient.ProcessCompleteUploadSync(context.Background(), tempFilePath, filename, dbPath, mimeType, b.cfg, true, b.username)
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

	srcFullPath := path.Join(srcDbPath, srcFilename)
	dstFullPath := path.Join(dstDbPath, dstFilename)

	// Strict Admin isolation
	if b.isAdmin && (isChildAccountPath(srcFullPath) || isChildAccountPath(dstFullPath)) {
		return gofakes3.CopyObjectResult{}, gofakes3.KeyNotFound(srcKey)
	}

	var file database.File
	query := "SELECT * FROM files WHERE path = ? AND filename = ? AND owner = ?"
	args := []interface{}{srcDbPath, srcFilename, b.username}
	if b.isAdmin {
		query = "SELECT * FROM files WHERE path = ? AND filename = ?"
		args = []interface{}{srcDbPath, srcFilename}
	}

	err := database.DB.Get(&file, query, args...)
	if err != nil {
		return gofakes3.CopyObjectResult{}, gofakes3.KeyNotFound(srcKey)
	}

	// Ensure destination directory exists
	database.EnsureFoldersExist(dstDbPath, b.username)

	// Perform the move/rename in database
	_, err = database.DB.Exec(
		"UPDATE files SET path = ?, filename = ? WHERE id = ?",
		dstDbPath, dstFilename, file.ID,
	)
	if err != nil {
		return gofakes3.CopyObjectResult{}, err
	}

	return gofakes3.CopyObjectResult{
		ETag:         fmt.Sprintf("\"%x\"", file.ID),
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
