package uploadsession

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"telecloud/database"
)

// Resume continues sessions interrupted by a restart. Staged data and
// confirmed chunks are durable, so no completed work is repeated: parts that
// were mid-upload go back to "ready", and parts whose last chunk was recorded
// just before a crash are promoted.
func (m *Manager) Resume() {
	var ids []string
	if err := database.RODB.Select(&ids, "SELECT id FROM upload_sessions WHERE state IN (?, ?)", StateOpen, StateFinalizing); err != nil {
		log.Printf("[Upload] resume: %v", err)
		return
	}
	for _, id := range ids {
		database.DB.Exec("UPDATE upload_session_parts SET state = ? WHERE session_id = ? AND state IN (?, ?)", partReady, id, partUploading, partFailed)
		if s, err := loadSession(id); err == nil {
			for i := int64(0); i < s.TotalParts; i++ {
				m.maybeReadyPart(s, i)
			}
		}
		m.kick(id)
	}
	if len(ids) > 0 {
		log.Printf("[Upload] resumed %d interrupted upload session(s)", len(ids))
	}
}

// StartCleanup periodically expires abandoned sessions.
func (m *Manager) StartCleanup() {
	go func() {
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		m.Cleanup()
		for {
			select {
			case <-t.C:
				m.Cleanup()
			case <-m.ctx.Done():
				return
			}
		}
	}()
}

// Cleanup removes temporary data of sessions idle for longer than the retention
// period. It only ever touches session tables, the staging directory, and
// Telegram parts that no published file references; finalized uploads lose only
// their bookkeeping row.
func (m *Manager) Cleanup() {
	cutoff := time.Now().Add(-m.cfg.ChunkedUpload.SessionTTL).Unix()

	var stale []Session
	if err := database.RODB.Select(&stale, "SELECT * FROM upload_sessions WHERE updated_at < ?", cutoff); err != nil {
		return
	}
	for _, s := range stale {
		switch s.State {
		case StateFinalized:
			database.DB.Exec("DELETE FROM upload_session_parts WHERE session_id = ?", s.ID)
			database.DB.Exec("DELETE FROM upload_sessions WHERE id = ?", s.ID)
		default:
			log.Printf("[Upload] session=%.8s expired (state=%s), discarding temporary data", s.ID, s.State)
			m.discard(s.ID)
			database.DB.Exec("DELETE FROM upload_sessions WHERE id = ?", s.ID)
		}
	}

	// Staging directories without a session row (e.g. after a manual DB reset).
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().Unix() > cutoff {
			continue
		}
		var n int
		if database.RODB.Get(&n, "SELECT COUNT(*) FROM upload_sessions WHERE id = ?", e.Name()) == nil && n == 0 {
			os.RemoveAll(filepath.Join(m.dir, e.Name()))
		}
	}
}
