package database

import (
	"path/filepath"
	"testing"
	"time"
)

// TestCleanupAbandonedFileRows pins the boundary of the placeholder sweep: it
// must remove rows a killed process left behind, and must not touch anything
// that is still in flight, is a folder, sits in the trash, already holds
// Telegram content, or has recorded parts.
func TestCleanupAbandonedFileRows(t *testing.T) {
	if err := InitDB("sqlite", filepath.Join(t.TempDir(), "t.db"), ""); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	recent := time.Now().UTC().Format("2006-01-02 15:04:05")

	ins := func(name string, msg, deleted interface{}, folder bool, created string) int64 {
		id, err := InsertAndGetID(DB,
			`INSERT INTO files (filename, path, size, is_folder, owner, message_id, deleted_at, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			name, "/u", 10, folder, "u", msg, deleted, created)
		if err != nil {
			t.Fatalf("insert %q: %v", name, err)
		}
		return id
	}

	// The one row the sweep is meant to remove: a placeholder from a run that
	// died before the first part reached Telegram.
	abandoned := ins("abandoned.mp4", nil, nil, false, old)

	// Everything below must survive.
	ins("inflight.mp4", nil, nil, false, recent) // upload still running
	ins("folder", nil, nil, true, old)           // folders have no message_id
	ins("trashed.mp4", nil, old, false, old)     // trash retention owns this
	ins("done.mp4", 12345, nil, false, old)      // already stored
	withParts := ins("parts.mp4", nil, nil, false, old)
	if _, err := DB.Exec(
		"INSERT INTO file_parts (file_id, message_id, part_index, size) VALUES (?, ?, ?, ?)",
		withParts, 999, 0, 10); err != nil {
		t.Fatal(err)
	}

	n, err := CleanupAbandonedFileRows(24 * time.Hour)
	if err != nil {
		t.Fatalf("CleanupAbandonedFileRows: %v", err)
	}

	var gone int
	if err := RODB.Get(&gone, "SELECT COUNT(*) FROM files WHERE id = ?", abandoned); err != nil {
		t.Fatal(err)
	}
	if gone != 0 {
		t.Error("abandoned placeholder was not deleted")
	}
	for _, name := range []string{"inflight.mp4", "folder", "trashed.mp4", "done.mp4", "parts.mp4"} {
		var kept int
		if err := RODB.Get(&kept, "SELECT COUNT(*) FROM files WHERE filename = ?", name); err != nil {
			t.Fatal(err)
		}
		if kept != 1 {
			t.Errorf("%q was wrongly deleted", name)
		}
	}
	if n != 1 {
		t.Errorf("deleted %d rows, want exactly 1", n)
	}

	// Running again must be a no-op: the sweep is called on every boot.
	if again, err := CleanupAbandonedFileRows(24 * time.Hour); err != nil || again != 0 {
		t.Errorf("second run deleted %d rows (err=%v), want 0", again, err)
	}
}
