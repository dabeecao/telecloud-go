package database

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// initTestDriver connects InitDB to whichever driver the environment selects,
// so the same assertions can run on SQLite, MySQL and PostgreSQL.
//
//	TC_TEST_DRIVER=mysql    TC_TEST_DSN='root@unix(/tmp/mysql-test.sock)/telecloud_test'
//	TC_TEST_DRIVER=postgres TC_TEST_DSN='postgres://user@localhost:5432/telecloud_test?sslmode=disable'
//
// With no environment it falls back to a throwaway SQLite file, so the default
// `go test ./...` stays hermetic.
func initTestDriver(t *testing.T) {
	t.Helper()
	driver := os.Getenv("TC_TEST_DRIVER")
	if driver == "" {
		driver = "sqlite"
	}
	dsn := os.Getenv("TC_TEST_DSN")
	if driver == "sqlite" {
		dsn = ""
		if err := InitDB("sqlite", filepath.Join(t.TempDir(), "t.db"), ""); err != nil {
			t.Fatal(err)
		}
		return
	}
	if dsn == "" {
		t.Fatalf("TC_TEST_DRIVER=%s requires TC_TEST_DSN", driver)
	}
	if err := InitDB(driver, "", dsn); err != nil {
		t.Fatalf("InitDB(%s): %v", driver, err)
	}
	// A shared server keeps whatever the previous run left behind, and this test
	// asserts on absolute row counts. Clear both ends.
	DB.Exec("DELETE FROM file_parts")
	DB.Exec("DELETE FROM files")
	t.Cleanup(func() {
		// Leave no rows behind for the next run against the same server.
		DB.Exec("DELETE FROM file_parts")
		DB.Exec("DELETE FROM files")
	})
}

// TestCleanupAbandonedFileRowsAllDrivers is the portable form of
// TestCleanupAbandonedFileRows: identical assertions, but it runs against
// whatever TC_TEST_DRIVER points at. The SQLite-only test stays as the hermetic
// default; this one is what actually proves RULE 5 (the statement works on
// MySQL and PostgreSQL).
func TestCleanupAbandonedFileRowsAllDrivers(t *testing.T) {
	initTestDriver(t)

	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now()

	ins := func(name string, msg, deleted interface{}, folder bool, created time.Time) int64 {
		id, err := InsertAndGetID(DB,
			`INSERT INTO files (filename, path, size, is_folder, owner, message_id, deleted_at, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			name, "/u", 10, folder, "u", msg, deleted, created)
		if err != nil {
			t.Fatalf("insert %q: %v", name, err)
		}
		return id
	}

	abandoned := ins("abandoned.mp4", nil, nil, false, old)
	ins("inflight.mp4", nil, nil, false, recent)
	ins("folder", nil, nil, true, old)
	ins("trashed.mp4", nil, old, false, old)
	ins("done.mp4", 12345, nil, false, old)
	withParts := ins("parts.mp4", nil, nil, false, old)
	if _, err := DB.Exec(
		"INSERT INTO file_parts (file_id, message_id, part_index, size) VALUES (?, ?, ?, ?)",
		withParts, 999, 0, 10); err != nil {
		t.Fatalf("insert file_parts: %v", err)
	}

	n, err := CleanupAbandonedFileRows(24 * time.Hour)
	if err != nil {
		t.Fatalf("CleanupAbandonedFileRows on %s: %v", driverName, err)
	}

	var gone int
	if err := RODB.Get(&gone, "SELECT COUNT(*) FROM files WHERE id = ?", abandoned); err != nil {
		t.Fatalf("verify delete: %v", err)
	}
	if gone != 0 {
		t.Errorf("[%s] abandoned placeholder was not deleted", driverName)
	}
	for _, name := range []string{"inflight.mp4", "folder", "trashed.mp4", "done.mp4", "parts.mp4"} {
		var kept int
		if err := RODB.Get(&kept, "SELECT COUNT(*) FROM files WHERE filename = ?", name); err != nil {
			t.Fatalf("verify %q: %v", name, err)
		}
		if kept != 1 {
			t.Errorf("[%s] %q was wrongly deleted", driverName, name)
		}
	}
	if n != 1 {
		t.Errorf("[%s] deleted %d rows, want exactly 1", driverName, n)
	}

	if again, err := CleanupAbandonedFileRows(24 * time.Hour); err != nil || again != 0 {
		t.Errorf("[%s] second run deleted %d rows (err=%v), want 0", driverName, again, err)
	}
	t.Logf("[%s] sweep behaves identically", driverName)
}
