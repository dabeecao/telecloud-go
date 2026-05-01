package webdav

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

type telecloudFS struct {
	cfg *config.Config
}

func NewTelecloudFS(cfg *config.Config) webdav.FileSystem {
	return &telecloudFS{cfg: cfg}
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

func isChildAccountPath(path string) bool {
	dir, base := splitPath(path)
	var rootFolder string
	if dir == "/" {
		rootFolder = base
	} else {
		parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		rootFolder = parts[0]
	}

	if rootFolder == "" {
		return false
	}

	var exists int
	database.DB.Get(&exists, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", rootFolder)
	return exists > 0
}

func (fs *telecloudFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	username, isAdmin := getUserInfo(ctx)
	dbName := mapPath(name, username, isAdmin)

	// Admin isolation check
	if isAdmin && isChildAccountPath(dbName) {
		return os.ErrPermission
	}

	dir, base := splitPath(dbName)
	
	// Check if parent directory exists
	if dir != "/" {
		var parent database.File
		pDir, pBase := splitPath(dir)
		err := database.DB.Get(&parent, "SELECT id FROM files WHERE path = ? AND filename = ? AND is_folder = 1", pDir, pBase)
		if err != nil {
			return os.ErrNotExist // maps to 409 Conflict in webdav
		}
	}

	_, err := database.DB.Exec("INSERT INTO files (filename, path, is_folder) VALUES (?, ?, 1)", base, dir)
	return err
}

func (fs *telecloudFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	username, isAdmin := getUserInfo(ctx)
	dbName := mapPath(name, username, isAdmin)
	
	// Admin isolation check
	if isAdmin && isChildAccountPath(dbName) {
		return nil, os.ErrPermission
	}

	dir, base := splitPath(dbName)

	var item database.File
	err := database.DB.Get(&item, "SELECT * FROM files WHERE path = ? AND filename = ?", dir, base)

	// Writing a new file
	if err != nil && (flag&os.O_CREATE) != 0 {
		return newFileWriter(ctx, fs.cfg, dir, base, false), nil
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
		}, nil
	}

	if (flag & os.O_WRONLY) != 0 || (flag & os.O_RDWR) != 0 {
		// Existing file being overwritten
		return newFileWriter(ctx, fs.cfg, dir, base, true), nil
	}

	// Reading an existing file
	var rs io.ReadSeeker
	if item.MessageID != nil {
		rs, err = tgclient.GetTelegramFileReader(ctx, *item.MessageID, item.Size, fs.cfg)
		if err != nil {
			return nil, err
		}
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
	}, nil
}

func (fs *telecloudFS) RemoveAll(ctx context.Context, name string) error {
	username, isAdmin := getUserInfo(ctx)
	dbName := mapPath(name, username, isAdmin)

	if dbName == "/" {
		return fmt.Errorf("cannot delete root")
	}

	// Admin isolation check
	if isAdmin && isChildAccountPath(dbName) {
		return os.ErrPermission
	}

	dir, base := splitPath(dbName)

	var item database.File
	if err := database.DB.Get(&item, "SELECT * FROM files WHERE path = ? AND filename = ?", dir, base); err != nil {
		return os.ErrNotExist
	}

	if item.IsFolder {
		oldPrefix := item.Path + "/" + item.Filename
		if item.Path == "/" {
			oldPrefix = "/" + item.Filename
		}
		var children []database.File
		database.DB.Select(&children, "SELECT message_id, thumb_path FROM files WHERE (path = ? OR path LIKE ?) AND message_id IS NOT NULL", oldPrefix, oldPrefix+"/%")

		var msgIDsToDelete []int
		for _, child := range children {
			if child.MessageID != nil {
				var count int
				database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE message_id = ?", *child.MessageID)
				if count <= 1 {
					msgIDsToDelete = append(msgIDsToDelete, *child.MessageID)
				}
			}
			if child.ThumbPath != nil {
				os.Remove(*child.ThumbPath)
			}
		}

		database.DB.Exec("DELETE FROM files WHERE path = ? OR path LIKE ?", oldPrefix, oldPrefix+"/%")
		database.DB.Exec("DELETE FROM files WHERE id = ?", item.ID)

		if len(msgIDsToDelete) > 0 {
			tgclient.DeleteMessages(ctx, fs.cfg, msgIDsToDelete)
		}
	} else {
		if item.MessageID != nil {
			var count int
			database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE message_id = ?", *item.MessageID)
			if count <= 1 {
				tgclient.DeleteMessages(ctx, fs.cfg, []int{*item.MessageID})
			}
		}
		if item.ThumbPath != nil {
			os.Remove(*item.ThumbPath)
		}
		database.DB.Exec("DELETE FROM files WHERE id = ?", item.ID)
	}

	return nil
}

func (fs *telecloudFS) Rename(ctx context.Context, oldName, newName string) error {
	username, isAdmin := getUserInfo(ctx)
	dbOldName := mapPath(oldName, username, isAdmin)
	dbNewName := mapPath(newName, username, isAdmin)

	// Admin isolation check
	if isAdmin && (isChildAccountPath(dbOldName) || isChildAccountPath(dbNewName)) {
		return os.ErrPermission
	}

	oldDir, oldBase := splitPath(dbOldName)
	newDir, newBase := splitPath(dbNewName)

	tx, err := database.DB.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var item database.File
	if err := tx.Get(&item, "SELECT * FROM files WHERE path = ? AND filename = ?", oldDir, oldBase); err != nil {
		return os.ErrNotExist
	}

	uniqueName := database.GetUniqueFilename(tx, newDir, newBase, item.IsFolder, item.ID)
	
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
		_, err = tx.Exec("UPDATE files SET path = ? || SUBSTR(path, ?) WHERE path = ? OR path LIKE ?", newPrefix, len(oldPrefix)+1, oldPrefix, oldPrefix+"/%")
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

	// Admin isolation check
	if isAdmin && isChildAccountPath(dbName) {
		return nil, os.ErrPermission
	}

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
	if err := database.DB.Get(&item, "SELECT * FROM files WHERE path = ? AND filename = ?", dir, base); err != nil {
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
	err := database.DB.Get(&thumbPath, "SELECT thumb_path FROM files WHERE path = ? AND filename = ? AND is_folder = 0", dir, base)
	if err != nil {
		return "", err
	}
	if thumbPath == nil {
		return "", os.ErrNotExist
	}
	return *thumbPath, nil
}
