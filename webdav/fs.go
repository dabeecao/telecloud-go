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

func (fs *telecloudFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	dir, base := splitPath(name)
	
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
	name = cleanPath(name)
	dir, base := splitPath(name)

	var item database.File
	err := database.DB.Get(&item, "SELECT * FROM files WHERE path = ? AND filename = ?", dir, base)

	// Writing a new file
	if err != nil && (flag&os.O_CREATE) != 0 {
		return newFileWriter(ctx, fs.cfg, dir, base, false), nil
	}

	if err != nil {
		// Root directory
		if name == "/" {
			return &telecloudFile{
				isDir: true,
				path:  "/",
				name:  "/",
			}, nil
		}
		return nil, os.ErrNotExist
	}

	if item.IsFolder {
		return &telecloudFile{
			isDir: true,
			path:  cleanPath(item.Path + "/" + item.Filename),
			name:  item.Filename,
			size:  0,
			mtime: item.CreatedAt,
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
		isDir: false,
		path:  dir,
		name:  item.Filename,
		size:  item.Size,
		mtime: item.CreatedAt,
		rs:    rs,
	}, nil
}

func (fs *telecloudFS) RemoveAll(ctx context.Context, name string) error {
	name = cleanPath(name)
	if name == "/" {
		return fmt.Errorf("cannot delete root")
	}
	dir, base := splitPath(name)

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
	oldDir, oldBase := splitPath(oldName)
	newDir, newBase := splitPath(newName)

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
	name = cleanPath(name)
	if name == "/" {
		return &telecloudFileInfo{
			name:  "/",
			size:  0,
			isDir: true,
			mtime: time.Now(),
		}, nil
	}
	dir, base := splitPath(name)

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
