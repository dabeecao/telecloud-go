package api

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"telecloud/config"
	"telecloud/database"
	"telecloud/tgclient"
	"telecloud/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type PasteRequest struct {
	Action      string `json:"action"`
	ItemIDs     []int  `json:"item_ids"`
	Destination string `json:"destination"`
}

func SetupRouter(cfg *config.Config, contentFS fs.FS) *gin.Engine {
	r := gin.Default()
	r.SetTrustedProxies(nil)
	
	templ := template.Must(template.New("").ParseFS(contentFS, "templates/*"))
	r.SetHTMLTemplate(templ)

	staticFS, err := fs.Sub(contentFS, "static")
	if err == nil {
		r.StaticFS("/static", http.FS(staticFS))
	}

	// Middleware for auth
	authMiddleware := func() gin.HandlerFunc {
		return func(c *gin.Context) {
			token, err := c.Cookie("session_token")
			if err != nil || token != cfg.AdminPassword {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
				return
			}
			c.Next()
		}
	}

	r.GET("/", func(c *gin.Context) {
		token, _ := c.Cookie("session_token")
		c.HTML(http.StatusOK, "index.html", gin.H{
			"logged_in":          token == cfg.AdminPassword,
			"max_upload_size_mb": cfg.MaxUploadSizeMB,
		})
	})

	r.POST("/login", func(c *gin.Context) {
		password := c.PostForm("password")
		if password == cfg.AdminPassword {
			c.SetCookie("session_token", password, 3600*24*30, "/", "", false, true)
			c.JSON(http.StatusOK, gin.H{"status": "success"})
			return
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
	})

	r.POST("/logout", func(c *gin.Context) {
		c.SetCookie("session_token", "", -1, "/", "", false, true)
		c.JSON(http.StatusOK, gin.H{"status": "success"})
	})

	api := r.Group("/api")
	api.Use(authMiddleware())
	{
		api.GET("/progress/:task_id", func(c *gin.Context) {
			taskID := c.Param("task_id")
			c.JSON(http.StatusOK, tgclient.GetTask(taskID))
		})

		api.GET("/files", func(c *gin.Context) {
			path := c.Query("path")
			if path == "" {
				path = "/"
			}
			var files []database.File
			err := database.DB.Select(&files, "SELECT * FROM files WHERE path = ? ORDER BY is_folder DESC, id DESC", path)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}

			for i := range files {
				if files[i].ShareToken != nil {
					files[i].DirectToken = utils.GenerateDirectToken(*files[i].ShareToken)
				}
				if files[i].ThumbPath != nil {
					if _, err := os.Stat(*files[i].ThumbPath); err == nil {
						files[i].HasThumb = true
					}
				}
			}
			c.JSON(http.StatusOK, gin.H{"files": files})
		})

		api.POST("/folders", func(c *gin.Context) {
			name := c.PostForm("name")
			path := c.PostForm("path")
			if path == "" {
				path = "/"
			}
			uniqueName := database.GetUniqueFilename(path, name, true)
			_, err := database.DB.Exec("INSERT INTO files (filename, path, is_folder) VALUES (?, ?, 1)", uniqueName, path)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.POST("/upload", func(c *gin.Context) {
			file, header, err := c.Request.FormFile("file")
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "No file"})
				return
			}
			defer file.Close()

			filename := c.PostForm("filename")
			path := c.PostForm("path")
			if path == "" {
				path = "/"
			}
			taskID := c.PostForm("task_id")
			chunkIndex, _ := strconv.Atoi(c.PostForm("chunk_index"))
			totalChunks, _ := strconv.Atoi(c.PostForm("total_chunks"))

			tempDir := filepath.Join(os.TempDir(), "telecloud_temp_chunks")
			os.MkdirAll(tempDir, os.ModePerm)
			tempFilePath := filepath.Join(tempDir, taskID+"_"+filename)

			out, err := os.OpenFile(tempFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			_, err = io.Copy(out, file)
			out.Close()

			if totalChunks == 0 {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "totalChunks is 0"})
				return
			}
			serverPercent := int((float64(chunkIndex+1) / float64(totalChunks)) * 100)
			tgclient.UpdateTask(taskID, "uploading_to_server", serverPercent, "")

			if chunkIndex == totalChunks-1 {
				mimeType := header.Header.Get("Content-Type")
				if mimeType == "" {
					mimeType = "application/octet-stream"
				}

				go func() {
					uniqueName := database.GetUniqueFilename(path, filename, false)
					tgclient.ProcessCompleteUpload(context.Background(), tempFilePath, uniqueName, path, mimeType, taskID, cfg)
					os.Remove(tempFilePath)
				}()

				c.JSON(http.StatusOK, gin.H{"status": "processing_telegram", "message": "Received all, pushing to Telegram"})
				return
			}

			c.JSON(http.StatusOK, gin.H{"status": "chunk_received", "chunk": chunkIndex})
		})

		api.POST("/actions/paste", func(c *gin.Context) {
			var req PasteRequest
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}

			tx, _ := database.DB.Beginx()
			for _, id := range req.ItemIDs {
				var item database.File
				err := tx.Get(&item, "SELECT * FROM files WHERE id = ?", id)
				if err != nil {
					continue
				}

				if item.IsFolder {
					oldPrefix := item.Path + "/" + item.Filename
					if item.Path == "/" {
						oldPrefix = "/" + item.Filename
					}
					if req.Destination == oldPrefix || strings.HasPrefix(req.Destination, oldPrefix+"/") {
						continue
					}
				}

				if req.Action == "move" {
					tx.Exec("UPDATE files SET path = ? WHERE id = ?", req.Destination, id)
					if item.IsFolder {
						oldPrefix := item.Path + "/" + item.Filename
						if item.Path == "/" {
							oldPrefix = "/" + item.Filename
						}
						newPrefix := req.Destination + "/" + item.Filename
						if req.Destination == "/" {
							newPrefix = "/" + item.Filename
						}
						tx.Exec("UPDATE files SET path = ? || SUBSTR(path, ?) WHERE path = ? OR path LIKE ?", newPrefix, len(oldPrefix)+1, oldPrefix, oldPrefix+"/%")
					}
				} else if req.Action == "copy" {
					if item.IsFolder {
						tx.Exec("INSERT INTO files (filename, path, is_folder) VALUES (?, ?, 1)", item.Filename, req.Destination)
						oldPrefix := item.Path + "/" + item.Filename
						if item.Path == "/" {
							oldPrefix = "/" + item.Filename
						}
						newPrefix := req.Destination + "/" + item.Filename
						if req.Destination == "/" {
							newPrefix = "/" + item.Filename
						}
						tx.Exec(`INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path, share_token)
                            SELECT message_id, filename, ? || SUBSTR(path, ?), size, mime_type, is_folder, thumb_path, NULL
                            FROM files WHERE path = ? OR path LIKE ?`, newPrefix, len(oldPrefix)+1, oldPrefix, oldPrefix+"/%")
					} else {
						uniqueName := database.GetUniqueFilename(req.Destination, item.Filename, false)
						tx.Exec("INSERT INTO files (message_id, filename, path, size, mime_type, is_folder, thumb_path) VALUES (?, ?, ?, ?, ?, 0, ?)", item.MessageID, uniqueName, req.Destination, item.Size, item.MimeType, item.ThumbPath)
					}
				}
			}
			tx.Commit()
			c.JSON(http.StatusOK, gin.H{"status": "success"})
		})

		api.DELETE("/files/:id", func(c *gin.Context) {
			id, _ := strconv.Atoi(c.Param("id"))
			
			var item database.File
			if err := database.DB.Get(&item, "SELECT * FROM files WHERE id = ?", id); err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
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
						if count <= 1 { // Only this one exists (or 0 if something went wrong)
							msgIDsToDelete = append(msgIDsToDelete, *child.MessageID)
						}
					}
					if child.ThumbPath != nil {
						var count int
						database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE thumb_path = ?", *child.ThumbPath)
						if count <= 1 {
							os.Remove(*child.ThumbPath)
						}
					}
				}
				
				database.DB.Exec("DELETE FROM files WHERE path = ? OR path LIKE ?", oldPrefix, oldPrefix+"/%")
				database.DB.Exec("DELETE FROM files WHERE id = ?", id)
				
				if len(msgIDsToDelete) > 0 {
					tgclient.DeleteMessages(context.Background(), cfg, msgIDsToDelete)
				}
			} else {
				if item.MessageID != nil {
					var count int
					database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE message_id = ?", *item.MessageID)
					if count <= 1 {
						tgclient.DeleteMessages(context.Background(), cfg, []int{*item.MessageID})
					}
				}
				if item.ThumbPath != nil {
					var count int
					database.DB.Get(&count, "SELECT COUNT(*) FROM files WHERE thumb_path = ?", *item.ThumbPath)
					if count <= 1 {
						os.Remove(*item.ThumbPath)
					}
				}
				database.DB.Exec("DELETE FROM files WHERE id = ?", id)
			}
			
			c.JSON(http.StatusOK, gin.H{"status": "deleted"})
		})

		api.PUT("/files/:id/rename", func(c *gin.Context) {
			id, _ := strconv.Atoi(c.Param("id"))
			newName := c.PostForm("new_name")

			var item database.File
			database.DB.Get(&item, "SELECT filename, path, is_folder FROM files WHERE id = ?", id)

			if !item.IsFolder {
				oldExt := filepath.Ext(item.Filename)
				newExt := filepath.Ext(newName)
				if oldExt != "" && newExt == "" {
					newName += oldExt
				}
			} else {
				basePath := item.Path
				oldPrefix := basePath + "/" + item.Filename
				if basePath == "/" {
					oldPrefix = "/" + item.Filename
				}
				newPrefix := basePath + "/" + newName
				if basePath == "/" {
					newPrefix = "/" + newName
				}
				database.DB.Exec("UPDATE files SET path = ? || SUBSTR(path, ?) WHERE path = ? OR path LIKE ?", newPrefix, len(oldPrefix)+1, oldPrefix, oldPrefix+"/%")
			}

			uniqueName := database.GetUniqueFilename(item.Path, newName, item.IsFolder)
			database.DB.Exec("UPDATE files SET filename = ? WHERE id = ?", uniqueName, id)
			c.JSON(http.StatusOK, gin.H{"status": "renamed"})
		})

		api.POST("/files/:id/share", func(c *gin.Context) {
			id, _ := strconv.Atoi(c.Param("id"))
			token := uuid.New().String()
			database.DB.Exec("UPDATE files SET share_token = ? WHERE id = ?", token, id)
			c.JSON(http.StatusOK, gin.H{"share_token": token, "direct_token": utils.GenerateDirectToken(token)})
		})

		api.DELETE("/files/:id/share", func(c *gin.Context) {
			id, _ := strconv.Atoi(c.Param("id"))
			database.DB.Exec("UPDATE files SET share_token = NULL WHERE id = ?", id)
			c.JSON(http.StatusOK, gin.H{"status": "revoked"})
		})

		api.GET("/files/:id/thumb", func(c *gin.Context) {
			id, _ := strconv.Atoi(c.Param("id"))
			var item database.File
			if err := database.DB.Get(&item, "SELECT thumb_path FROM files WHERE id = ?", id); err != nil || item.ThumbPath == nil {
				c.AbortWithStatus(http.StatusNotFound)
				return
			}
			c.File(*item.ThumbPath)
		})
	}

	r.GET("/download/:id", authMiddleware(), func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		var item database.File
		if err := database.DB.Get(&item, "SELECT message_id, filename, mime_type, size FROM files WHERE id = ?", id); err != nil || item.MessageID == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
			return
		}

		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, item.Filename))
		if item.MimeType != nil {
			c.Header("Content-Type", *item.MimeType)
		}
		c.SetCookie("dl_started", "1", 15, "/", "", false, false)

		if err := tgclient.ServeTelegramFile(c.Request, c.Writer, *item.MessageID, item.Filename, item.Size, cfg); err != nil {
			// Handle error
			fmt.Println("Stream error:", err)
		}
	})

	r.GET("/dl/:token", func(c *gin.Context) {
		directToken := c.Param("token")
		shareToken := utils.VerifyDirectToken(directToken)
		if shareToken == nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "Invalid token"})
			return
		}

		var item database.File
		if err := database.DB.Get(&item, "SELECT message_id, filename, mime_type, size FROM files WHERE share_token = ?", *shareToken); err != nil || item.MessageID == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
			return
		}

		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, item.Filename))
		if item.MimeType != nil {
			c.Header("Content-Type", *item.MimeType)
		}

		if err := tgclient.ServeTelegramFile(c.Request, c.Writer, *item.MessageID, item.Filename, item.Size, cfg); err != nil {
			fmt.Println("Stream error:", err)
		}
	})

	r.GET("/s/:token", func(c *gin.Context) {
		token := c.Param("token")
		var item database.File
		if err := database.DB.Get(&item, "SELECT filename, size, created_at, thumb_path FROM files WHERE share_token = ?", token); err != nil {
			c.HTML(http.StatusNotFound, "error.html", gin.H{"error_message": "File not found or link has been revoked."})
			return
		}
		
		hasThumb := false
		if item.ThumbPath != nil {
			if _, err := os.Stat(*item.ThumbPath); err == nil {
				hasThumb = true
			}
		}
		
		c.HTML(http.StatusOK, "share.html", gin.H{
			"filename": item.Filename,
			"size": item.Size,
			"created_at": item.CreatedAt.Format("2006-01-02 15:04:05"),
			"token": token,
			"has_thumb": hasThumb,
		})
	})

	r.GET("/s/:token/stream", func(c *gin.Context) {
		token := c.Param("token")
		var item database.File
		if err := database.DB.Get(&item, "SELECT message_id, filename, size, mime_type FROM files WHERE share_token = ?", token); err != nil || item.MessageID == nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		
		if item.MimeType != nil {
			c.Header("Content-Type", *item.MimeType)
		}
		
		tgclient.ServeTelegramFile(c.Request, c.Writer, *item.MessageID, item.Filename, item.Size, cfg)
	})

	r.POST("/s/:token/dl", func(c *gin.Context) {
		token := c.Param("token")
		var item database.File
		if err := database.DB.Get(&item, "SELECT message_id, filename, size, mime_type FROM files WHERE share_token = ?", token); err != nil || item.MessageID == nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, item.Filename))
		if item.MimeType != nil {
			c.Header("Content-Type", *item.MimeType)
		}
		
		tgclient.ServeTelegramFile(c.Request, c.Writer, *item.MessageID, item.Filename, item.Size, cfg)
	})

	r.GET("/s/:token/thumb", func(c *gin.Context) {
		token := c.Param("token")
		var item database.File
		if err := database.DB.Get(&item, "SELECT thumb_path FROM files WHERE share_token = ?", token); err != nil || item.ThumbPath == nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.File(*item.ThumbPath)
	})

	return r
}
