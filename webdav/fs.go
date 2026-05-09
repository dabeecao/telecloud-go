package webdav

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"telecloud/config"
	"telecloud/database"
	"telecloud/tgclient"

	"golang.org/x/net/webdav"
)

type contextKey string

const (
	usernameKey contextKey = "username"
	isAdminKey  contextKey = "is_admin"
)

type dirCacheEntry struct {
	items     []os.FileInfo
	expiresAt time.Time
}

type telecloudFS struct {
	cfg      *config.Config
	dirCache sync.Map // map[string]*dirCacheEntry keyed by username + ":" + path
}

func NewTelecloudFS(cfg *config.Config) webdav.FileSystem {
	return &telecloudFS{
		cfg: cfg,
	}
}

// cleanPath ensures paths start with / and don't end with /
func cleanPath(p string) string {
	p = filepath.Clean(p)
	if p == "." || p == "" {
		return "/"
	}
	return p
}

// splitPath splits a path into parent directory and filename
func splitPath(p string) (string, string) {
	p = cleanPath(p)
	if p == "/" {
		return "/", ""
	}
	dir := filepath.Dir(p)
	base := filepath.Base(p)
	return dir, base
}

func mapPath(userPath, username string, isAdmin bool) string {
	cleanPath := filepath.Clean("/" + userPath)
	if isAdmin {
		return cleanPath
	}
	if cleanPath == "/" {
		return "/" + username
	}
	return "/" + username + cleanPath
}

func unmapPath(dbPath, username string, isAdmin bool) string {
	if isAdmin {
		return dbPath
	}
	prefix := "/" + username
	if dbPath == prefix {
		return "/"
	}
	if strings.HasPrefix(dbPath, prefix+"/") {
		return strings.TrimPrefix(dbPath, prefix)
	}
	return dbPath
}

func getUserInfo(ctx context.Context) (string, bool) {
	username, _ := ctx.Value(usernameKey).(string)
	isAdmin, _ := ctx.Value(isAdminKey).(bool)
	return username, isAdmin
}

func (fs *telecloudFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	username, isAdmin := getUserInfo(ctx)
	dbName := mapPath(name, username, isAdmin)

	dir, base := splitPath(dbName)

	// Admin root collision check
	if isAdmin && dir == "/" {
		var count int
		database.RODB.Get(&count, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", base)
		if count > 0 {
			return os.ErrPermission
		}
	}

	// Check if parent directory exists
	if dir != "/" {
		var parent database.File
		pDir, pBase := splitPath(dir)
		err := database.RODB.Get(&parent, "SELECT id FROM files WHERE path = ? AND filename = ? AND is_folder = 1 AND owner = ?", pDir, pBase, username)
		if err != nil {
			return os.ErrNotExist // maps to 409 Conflict in webdav
		}
	}

	_, err := database.DB.Exec("INSERT INTO files (filename, path, is_folder, owner) VALUES (?, ?, 1, ?)", base, dir, username)
	return err
}

func (fs *telecloudFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	username, isAdmin := getUserInfo(ctx)
	dbName := mapPath(name, username, isAdmin)

	dir, base := splitPath(dbName)

	var item database.File
	err := database.RODB.Get(&item, "SELECT * FROM files WHERE path = ? AND filename = ? AND owner = ?", dir, base, username)

	// Writing a new file
	if err != nil && (flag&os.O_CREATE) != 0 {
		return newFileWriter(ctx, fs.cfg, dir, base, false, username), nil
	}

	if err != nil {
		// Root directory for Admin or any non-existent path that maps to root
		if dbName == "/" || name == "/" || name == "" {
			return &telecloudFile{
				isDir:    true,
				path:     dbName,
				name:     "/",
				isAdmin:  isAdmin,
				username: username,
				fs:       fs,
			}, nil
		}
		return nil, os.ErrNotExist
	}

	if item.IsFolder {
		fileName := item.Filename
		if name == "/" || name == "" {
			fileName = "/"
		}
		return &telecloudFile{
			isDir:    true,
			path:     cleanPath(item.Path + "/" + item.Filename),
			name:     fileName,
			size:     0,
			mtime:    item.CreatedAt,
			isAdmin:  isAdmin,
			username: username,
			fs:       fs,
		}, nil
	}

	if (flag&os.O_WRONLY) != 0 || (flag&os.O_RDWR) != 0 {
		// Existing file being overwritten
		return newFileWriter(ctx, fs.cfg, dir, base, true, username), nil
	}

	// Reading an existing file
	// If file has no message_id and no file_parts, it's still being uploaded to Telegram
	if item.MessageID == nil {
		parts, err := database.GetFileParts(item.ID)
		if err != nil || len(parts) == 0 {
			return nil, os.ErrNotExist
		}
	}

	var rs io.ReadSeekCloser
	rs, err = tgclient.GetTelegramFileReader(ctx, item, fs.cfg)
	if err != nil {
		return nil, err
	}

	return &telecloudFile{
		isDir:    false,
		path:     dir,
		name:     item.Filename,
		size:     item.Size,
		mtime:    item.CreatedAt,
		rs:       rs,
		isAdmin:  isAdmin,
		username: username,
		fs:       fs,
	}, nil
}

func (fs *telecloudFS) RemoveAll(ctx context.Context, name string) error {
	username, isAdmin := getUserInfo(ctx)
	dbName := mapPath(name, username, isAdmin)

	if dbName == "/" {
		return fmt.Errorf("cannot delete root")
	}

	dir, base := splitPath(dbName)

	var item database.File
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE path = ? AND filename = ? AND owner = ?", dir, base, username); err != nil {
		return os.ErrNotExist
	}

	var fileIDs []int
	if item.IsFolder {
		oldPrefix := item.Path + "/" + item.Filename
		if item.Path == "/" {
			oldPrefix = "/" + item.Filename
		}
		database.RODB.Select(&fileIDs, "SELECT id FROM files WHERE (path = ? OR path LIKE ?) AND owner = ?", oldPrefix, oldPrefix+"/%", username)
	}
	fileIDs = append(fileIDs, item.ID)

	// Identify messages to delete from Telegram before removing files from DB
	msgIDsToDelete, _ := database.GetOrphanedMessages(fileIDs)

	// Delete thumbnails — single query: only get thumb paths exclusively owned by files being deleted
	if len(fileIDs) > 0 {
		placeholders := make([]string, len(fileIDs))
		args := make([]interface{}, len(fileIDs))
		for i, id := range fileIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		var thumbsToDelete []string
		database.RODB.Select(&thumbsToDelete, fmt.Sprintf(
			`SELECT thumb_path FROM files WHERE id IN (%s) AND thumb_path IS NOT NULL
			 AND (SELECT COUNT(*) FROM files f2 WHERE f2.thumb_path = files.thumb_path) = 1`,
			strings.Join(placeholders, ","),
		), args...)
		for _, tp := range thumbsToDelete {
			os.Remove(tp)
		}
	}

	// Delete from DB
	if item.IsFolder {
		oldPrefix := item.Path + "/" + item.Filename
		if item.Path == "/" {
			oldPrefix = "/" + item.Filename
		}
		database.DB.Exec("DELETE FROM files WHERE (path = ? OR path LIKE ?) AND owner = ?", oldPrefix, oldPrefix+"/%", username)
	}
	database.DB.Exec("DELETE FROM files WHERE id = ?", item.ID)

	// Delete from Telegram
	if len(msgIDsToDelete) > 0 {
		tgclient.DeleteMessages(ctx, fs.cfg, msgIDsToDelete)
	}

	return nil
}

func (fs *telecloudFS) Rename(ctx context.Context, oldName, newName string) error {
	username, isAdmin := getUserInfo(ctx)
	dbOldName := mapPath(oldName, username, isAdmin)
	dbNewName := mapPath(newName, username, isAdmin)

	oldDir, oldBase := splitPath(dbOldName)
	newDir, newBase := splitPath(dbNewName)

	// Admin root collision check
	if isAdmin && newDir == "/" {
		var count int
		database.RODB.Get(&count, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", newBase)
		if count > 0 {
			return os.ErrPermission
		}
	}

	tx, err := database.DB.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var item database.File
	if err := tx.Get(&item, "SELECT * FROM files WHERE path = ? AND filename = ? AND owner = ?", oldDir, oldBase, username); err != nil {
		return os.ErrNotExist
	}

	uniqueName := database.GetUniqueFilename(tx, newDir, newBase, item.IsFolder, item.ID, username)

	if item.IsFolder {
		oldPrefix := item.Path + "/" + item.Filename
		if item.Path == "/" {
			oldPrefix = "/" + item.Filename
		}

		// Prevent moving folder into itself or its own subfolder
		if newDir == oldPrefix || strings.HasPrefix(newDir, oldPrefix+"/") {
			return fmt.Errorf("cannot move folder into itself")
		}

		newPrefix := newDir + "/" + uniqueName
		if newDir == "/" {
			newPrefix = "/" + uniqueName
		}
		_, err = tx.Exec("UPDATE files SET path = "+database.ConcatPathSQL()+" WHERE (path = ? OR path LIKE ?) AND owner = ?", newPrefix, len(oldPrefix)+1, oldPrefix, oldPrefix+"/%", username)
		if err != nil {
			return err
		}
	}

	_, err = tx.Exec("UPDATE files SET filename = ?, path = ? WHERE id = ?", uniqueName, newDir, item.ID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (fs *telecloudFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	username, isAdmin := getUserInfo(ctx)
	dbName := mapPath(name, username, isAdmin)

	if dbName == "/" || name == "/" || name == "" {
		return &telecloudFileInfo{
			name:  "/",
			size:  0,
			isDir: true,
			mtime: time.Now(),
		}, nil
	}
	dir, base := splitPath(dbName)

	var item database.File
	if err := database.RODB.Get(&item, "SELECT * FROM files WHERE path = ? AND filename = ? AND owner = ?", dir, base, username); err != nil {
		return nil, os.ErrNotExist
	}

	return &telecloudFileInfo{
		name:  item.Filename,
		size:  item.Size,
		isDir: item.IsFolder,
		mtime: item.CreatedAt,
	}, nil
}

func (fs *telecloudFS) GetThumbnailPath(ctx context.Context, name string) (string, error) {
	username, isAdmin := getUserInfo(ctx)
	dbName := mapPath(name, username, isAdmin)

	dir, base := splitPath(dbName)

	var thumbPath *string
	err := database.RODB.Get(&thumbPath, "SELECT thumb_path FROM files WHERE path = ? AND filename = ? AND is_folder = 0 AND owner = ?", dir, base, username)
	if err != nil {
		return "", err
	}
	if thumbPath == nil {
		return "", os.ErrNotExist
	}
	return *thumbPath, nil
}
