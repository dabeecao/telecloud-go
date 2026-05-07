package api

import (
	"fmt"
	"path"
	"strings"
	"telecloud/database"

	"github.com/gin-gonic/gin"
)

func mapPath(userPath, username string, isAdmin bool) string {
	cleanPath := path.Clean("/" + userPath)
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

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func verifyItemAccess(c *gin.Context, path string) bool {
	isAdmin := c.GetBool("is_admin")
	if isAdmin {
		return true
	}
	username := c.GetString("username")
	prefix := "/" + username
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func isChildAccountPath(dbPath string) bool {
	return isChildAccountPathQuery(database.DB, dbPath)
}

func isChildAccountPathQuery(q database.Queryer, dbPath string) bool {
	cleanPath := path.Clean(dbPath)
	if cleanPath == "/" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(cleanPath, "/"), "/")
	rootFolder := parts[0]

	var exists int
	q.Get(&exists, "SELECT COUNT(*) FROM child_accounts WHERE username = ?", rootFolder)
	return exists > 0
}
