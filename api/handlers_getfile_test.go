package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"telecloud/database"

	"github.com/gin-gonic/gin"
)

// TestGetFileReturnsShareTokenForOwner covers the call telecloud-app makes to
// read an existing share_token before creating a new share. It must return the
// owner's row with the token, and must not return rows owned by someone else
// or rows that are in the trash.
func TestGetFileReturnsShareTokenForOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	driver, dsn := testDriverEnv()
	if driver == "sqlite" {
		if err := database.InitDB("sqlite", t.TempDir()+"/t.db", ""); err != nil {
			t.Fatal(err)
		}
	} else if err := database.InitDB(driver, "", dsn); err != nil {
		t.Fatalf("InitDB(%s): %v", driver, err)
	}
	defer database.RWDB.Close()
	defer database.RODB.Close()
	if driver != "sqlite" {
		database.DB.Exec("DELETE FROM file_parts")
		database.DB.Exec("DELETE FROM files")
		defer func() {
			database.DB.Exec("DELETE FROM file_parts")
			database.DB.Exec("DELETE FROM files")
		}()
	}

	const user = "alice"
	token := "tok-123"
	res, err := database.DB.Exec(
		`INSERT INTO files (filename, path, size, is_folder, owner, message_id, share_token)
		 VALUES ('shared.mp4', '/alice/docs', 10, FALSE, ?, 4242, ?)`, user, token)
	if err != nil {
		t.Fatalf("insert shared: %v", err)
	}
	sharedID, _ := res.LastInsertId()

	res, err = database.DB.Exec(
		`INSERT INTO files (filename, path, size, is_folder, owner, message_id, share_token)
		 VALUES ('other.mp4', '/bob/docs', 10, FALSE, 'bob', 4243, 'bob-tok')`)
	if err != nil {
		t.Fatalf("insert other: %v", err)
	}
	otherID, _ := res.LastInsertId()

	if _, err := database.DB.Exec(
		`INSERT INTO files (filename, path, size, is_folder, owner, message_id, share_token, deleted_at)
		 VALUES ('trashed.mp4', '/alice/docs', 10, FALSE, ?, 4244, 'trash-tok', '2024-01-01 00:00:00')`, user); err != nil {
		t.Fatalf("insert trashed: %v", err)
	}
	var trashedID int64
	database.DB.Get(&trashedID, "SELECT id FROM files WHERE filename = 'trashed.mp4'")

	h := &Handler{}
	r := gin.New()
	g := r.Group("/api", func(c *gin.Context) {
		c.Set("username", user)
		c.Set("is_admin", false)
	})
	g.GET("/files/:id", h.handleGetFile)

	get := func(id int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/files/"+strconv.FormatInt(id, 10), nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w := get(sharedID)
	if w.Code != http.StatusOK {
		t.Fatalf("owner shared file: status = %d, body = %s", w.Code, w.Body.String())
	}
	var got struct {
		ShareToken  *string `json:"share_token"`
		DirectToken string  `json:"direct_token"`
		Path        string  `json:"path"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ShareToken == nil || *got.ShareToken != token {
		t.Errorf("share_token = %v, want %q", got.ShareToken, token)
	}
	if got.DirectToken == "" {
		t.Errorf("direct_token should be set for a shared file")
	}
	if got.Path != "/docs" {
		t.Errorf("path = %q, want %q (user-facing path)", got.Path, "/docs")
	}

	for name, id := range map[string]int64{"other user's file": otherID, "trashed file": trashedID} {
		if w := get(id); w.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404; body = %s", name, w.Code, w.Body.String())
		}
	}
}
