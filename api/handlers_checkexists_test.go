package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"telecloud/database"

	"github.com/gin-gonic/gin"
)

// TestCheckExistsIgnoresInvisibleRows pins the collision dialog to the same
// visibility rule the file listings use. A row without a message_id (an upload
// still in flight, or a placeholder left by a killed process) and a row in the
// trash are both invisible in the UI, so reporting them as "already exists"
// would prompt the user about a file they cannot open.
func TestCheckExistsIgnoresInvisibleRows(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := database.InitDB("sqlite", t.TempDir()+"/t.db", ""); err != nil {
		t.Fatal(err)
	}
	defer database.RWDB.Close()
	defer database.RODB.Close()

	const user = "alice"
	const dir = "/alice/docs"

	ins := func(name string, msg, deleted interface{}) {
		if _, err := database.DB.Exec(
			`INSERT INTO files (filename, path, size, is_folder, owner, message_id, deleted_at)
			 VALUES (?, ?, 10, FALSE, ?, ?, ?)`, name, dir, user, msg, deleted); err != nil {
			t.Fatalf("insert %q: %v", name, err)
		}
	}
	ins("stored.mp4", 4242, nil)                    // real file: must be reported
	ins("inflight.mp4", nil, nil)                   // upload still running: must be ignored
	ins("trashed.mp4", 4243, "2024-01-01 00:00:00") // in the trash: must be ignored

	h := &Handler{}
	r := gin.New()
	g := r.Group("/api", func(c *gin.Context) {
		c.Set("username", user)
		c.Set("is_admin", false)
	})
	g.POST("/upload/check-exists", h.handlePostCheckExists)

	form := url.Values{}
	form.Set("path", "/docs")
	form.Set("filenames", "stored.mp4|inflight.mp4|trashed.mp4|missing.mp4")
	req := httptest.NewRequest(http.MethodPost, "/api/upload/check-exists", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "stored.mp4") {
		t.Errorf("stored.mp4 should be reported as existing; got %s", body)
	}
	for _, absent := range []string{"inflight.mp4", "trashed.mp4", "missing.mp4"} {
		if strings.Contains(body, absent) {
			t.Errorf("%s must not be reported as existing; got %s", absent, body)
		}
	}
}
