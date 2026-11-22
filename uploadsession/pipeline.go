package uploadsession

import (
	"context"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"telecloud/database"
	"telecloud/tgclient"
)

// maxPartRounds bounds how many times a part is handed to the Telegram bridge
// (each round already performs MaxRetries attempts with backoff) before the
// whole session is failed.
const maxPartRounds = 5

// kick schedules a pass of the per-session pipeline. Passes never overlap.
func (m *Manager) kick(id string) {
	rt := m.runtime(id)
	rt.mu.Lock()
	rt.dirty = true
	if rt.pumping {
		rt.mu.Unlock()
		return
	}
	rt.pumping = true
	rt.mu.Unlock()
	if !m.spawn(func() { m.pump(rt) }) {
		rt.mu.Lock()
		rt.pumping = false
		rt.mu.Unlock()
	}
}

func (m *Manager) pump(rt *runtime) {
	for {
		rt.mu.Lock()
		if !rt.dirty || rt.ctx.Err() != nil {
			rt.pumping = false
			rt.mu.Unlock()
			return
		}
		rt.dirty = false
		rt.mu.Unlock()
		m.step(rt)
	}
}

func (m *Manager) step(rt *runtime) {
	s, err := loadSession(rt.id)
	if err != nil || (s.State != StateOpen && s.State != StateFinalizing) {
		return
	}
	if err := m.advanceHash(rt, s); err != nil {
		switch {
		case rt.ctx.Err() != nil:
			// Cancelled or shutting down: never mistake that for data loss.
		case errors.Is(err, os.ErrNotExist) || errors.Is(err, errStagingSize):
			m.fail(rt.id, "staged data is missing or damaged")
		default:
			// Transient (EMFILE, EIO, busy DB): keep the session, try again.
			log.Printf("[Upload] session=%.8s hashing will be retried: %v", rt.id, err)
			time.AfterFunc(10*time.Second, func() { m.kick(rt.id) })
		}
		return
	}
	m.startUploads(rt, s)
	if s.State == StateFinalizing {
		m.tryPublish(rt, s)
	}
}

// advanceHash extends the whole-file SHA-256 over the next complete parts, in
// order. The hasher state is persisted after every part, so hashing survives
// restarts without keeping the data around.
func (m *Manager) advanceHash(rt *runtime, s *Session) error {
	for s.HashedParts < s.TotalParts {
		p, err := loadPart(s.ID, s.HashedParts)
		if err != nil {
			return err
		}
		if p.State != partReady && p.State != partUploading && p.State != partUploaded {
			return nil // not all chunks of this part have arrived yet
		}
		h := sha256.New()
		if len(s.HashState) > 0 {
			if err := h.(encoding.BinaryUnmarshaler).UnmarshalBinary(s.HashState); err != nil {
				return err
			}
		}
		f, err := os.Open(m.partPath(s.ID, p.Index))
		if err != nil {
			return err
		}
		if st, err := f.Stat(); err != nil || st.Size() != p.Size {
			f.Close()
			return fmt.Errorf("part %d: %w", p.Index, errStagingSize)
		}
		_, err = io.Copy(h, &ctxReader{ctx: rt.ctx, r: io.NewSectionReader(f, 0, p.Size)})
		f.Close()
		if err != nil {
			return err
		}
		state, err := h.(encoding.BinaryMarshaler).MarshalBinary()
		if err != nil {
			return err
		}
		if _, err := database.DB.Exec("UPDATE upload_sessions SET hash_state = ?, hashed_parts = ?, updated_at = ? WHERE id = ? AND hashed_parts = ?",
			state, p.Index+1, now(), s.ID, p.Index); err != nil {
			return err
		}
		s.HashState, s.HashedParts = state, p.Index+1
		m.release(rt, s.ID, p.Index)
	}
	return nil
}

var errStagingSize = errors.New("staging file has wrong size")

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// release deletes a part's staging file once it is both hashed and stored in
// Telegram, which is what keeps local disk use independent of the file size.
func (m *Manager) release(rt *runtime, id string, idx int64) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	s, err := loadSession(id)
	if err != nil || s.HashedParts <= idx {
		return
	}
	p, err := loadPart(id, idx)
	if err != nil || p.State != partUploaded {
		return
	}
	os.Remove(m.partPath(id, idx))
}

func (m *Manager) startUploads(rt *runtime, s *Session) {
	parts, err := loadParts(s.ID)
	if err != nil {
		return
	}
	for _, p := range parts {
		if p.State != partReady {
			continue
		}
		rt.mu.Lock()
		if rt.running[p.Index] || time.Now().Before(rt.retryAt[p.Index]) {
			rt.mu.Unlock()
			continue
		}
		rt.running[p.Index] = true
		rt.mu.Unlock()
		pp := p
		m.spawn(func() { m.uploadPart(rt, s, pp) })
	}
}

func (m *Manager) uploadPart(rt *runtime, s *Session, p Part) {
	defer func() {
		rt.mu.Lock()
		delete(rt.running, p.Index)
		rt.mu.Unlock()
		m.kick(s.ID)
	}()

	select {
	case m.partSem <- struct{}{}:
		defer func() { <-m.partSem }()
	case <-rt.ctx.Done():
		return
	}

	res, err := database.DB.Exec("UPDATE upload_session_parts SET state = ?, attempts = attempts + 1, updated_at = ? WHERE session_id = ? AND part_index = ? AND state = ?",
		partUploading, now(), s.ID, p.Index, partReady)
	if err != nil {
		return
	}
	if af, _ := res.RowsAffected(); af == 0 {
		return
	}

	f, err := os.Open(m.partPath(s.ID, p.Index))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			m.fail(s.ID, fmt.Sprintf("part %d staging file missing", p.Index))
			return
		}
		// Transient open error: put the part back and retry shortly.
		log.Printf("[Upload] session=%.8s part=%d open failed, will retry: %v", s.ID, p.Index, err)
		database.DB.Exec("UPDATE upload_session_parts SET state = ?, attempts = attempts - 1 WHERE session_id = ? AND part_index = ? AND state = ?", partReady, s.ID, p.Index, partUploading)
		time.AfterFunc(10*time.Second, func() { m.kick(s.ID) })
		return
	}
	defer f.Close()

	name := s.Filename
	if s.TotalParts > 1 {
		name = fmt.Sprintf("%s.part%d", s.Filename, p.Index+1)
	}
	started := time.Now()
	msgID, err := m.bridge.UploadPart(rt.ctx, f, p.Size, name, s.Filename, m.cfg.ChunkedUpload.MaxRetries, func(r Retry) {
		database.DB.Exec("UPDATE upload_sessions SET retry_count = retry_count + 1 WHERE id = ?", s.ID)
		database.DB.Exec("UPDATE upload_session_parts SET last_error = ? WHERE session_id = ? AND part_index = ?", clip(r.Err.Error()), s.ID, p.Index)
		log.Printf("[Upload] session=%.8s part=%d attempt=%d failed (flood_wait=%t), retrying in %s", s.ID, p.Index, r.Attempt, r.FloodWait, r.Wait.Round(time.Second))
	})
	if err != nil {
		if rt.ctx.Err() != nil {
			database.DB.Exec("UPDATE upload_session_parts SET state = ? WHERE session_id = ? AND part_index = ? AND state = ?", partReady, s.ID, p.Index, partUploading)
			return
		}
		m.partFailed(rt, s, p, err)
		return
	}

	// The session may have been cancelled or expired while uploading; a part
	// stored for a dead session must not leak into the log group.
	if cur, err := loadSession(s.ID); err != nil || (cur.State != StateOpen && cur.State != StateFinalizing) {
		go m.bridge.DeleteMessages(context.Background(), []int{msgID})
		return
	}
	res, err = database.DB.Exec("UPDATE upload_session_parts SET state = ?, message_id = ?, last_error = NULL, updated_at = ? WHERE session_id = ? AND part_index = ?",
		partUploaded, msgID, now(), s.ID, p.Index)
	if err != nil {
		log.Printf("[Upload] session=%.8s part=%d: recording message failed: %v", s.ID, p.Index, err)
		go m.bridge.DeleteMessages(context.Background(), []int{msgID})
		return
	}
	if af, _ := res.RowsAffected(); af == 0 {
		// The session was discarded between the check above and now.
		go m.bridge.DeleteMessages(context.Background(), []int{msgID})
		return
	}
	log.Printf("[Upload] session=%.8s part=%d stored (%d bytes in %s)", s.ID, p.Index, p.Size, time.Since(started).Round(time.Second))
	f.Close() // release() removes the file; open handles block that on Windows
	m.release(rt, s.ID, p.Index)
}

func (m *Manager) partFailed(rt *runtime, s *Session, p Part, err error) {
	if errors.Is(err, tgclient.ErrTelegramNotReady) {
		// Telegram is still connecting (e.g. right after a restart): not the
		// part's fault, so do not spend one of its attempts.
		database.DB.Exec("UPDATE upload_session_parts SET state = ?, attempts = attempts - 1, updated_at = ? WHERE session_id = ? AND part_index = ?", partReady, now(), s.ID, p.Index)
		time.AfterFunc(5*time.Second, func() { m.kick(s.ID) })
		return
	}
	cur, _ := loadPart(s.ID, p.Index)
	rounds := int64(1)
	if cur != nil {
		rounds = cur.Attempts
	}
	database.DB.Exec("UPDATE upload_session_parts SET state = ?, last_error = ?, updated_at = ? WHERE session_id = ? AND part_index = ?",
		partFailed, clip(err.Error()), now(), s.ID, p.Index)
	if rounds >= maxPartRounds {
		m.fail(s.ID, fmt.Sprintf("part %d could not be stored in Telegram: %s", p.Index, clip(err.Error())))
		return
	}
	delay := time.Duration(30<<uint(rounds-1)) * time.Second
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	log.Printf("[Upload] session=%.8s part=%d round %d failed, next round in %s", s.ID, p.Index, rounds, delay)
	rt.mu.Lock()
	rt.retryAt[p.Index] = time.Now().Add(delay)
	rt.mu.Unlock()
	time.AfterFunc(delay, func() {
		database.DB.Exec("UPDATE upload_session_parts SET state = ? WHERE session_id = ? AND part_index = ? AND state = ?", partReady, s.ID, p.Index, partFailed)
		m.kick(s.ID)
	})
}

// tryPublish verifies the finished upload and publishes it as a file.
func (m *Manager) tryPublish(rt *runtime, s *Session) {
	if s.HashedParts != s.TotalParts {
		return
	}
	parts, err := loadParts(s.ID)
	if err != nil || int64(len(parts)) != s.TotalParts {
		return
	}
	for _, p := range parts {
		if p.State != partUploaded || p.MessageID == nil {
			return
		}
	}
	n, bytes, err := countChunks(s.ID)
	if err != nil {
		return
	}
	if n != s.TotalChunks || bytes != s.TotalSize {
		m.fail(s.ID, "size verification failed")
		return
	}
	h := sha256.New()
	if err := h.(encoding.BinaryUnmarshaler).UnmarshalBinary(s.HashState); err != nil {
		m.fail(s.ID, "hash state corrupt")
		return
	}
	if s.ExpectedSHA256 == nil || hex.EncodeToString(h.Sum(nil)) != *s.ExpectedSHA256 {
		m.fail(s.ID, "whole-file checksum mismatch")
		return
	}

	fileID, _, err := m.publish(s, parts)
	if err != nil {
		log.Printf("[Upload] session=%.8s publish failed, will retry: %v", s.ID, err)
		time.AfterFunc(30*time.Second, func() { m.kick(s.ID) })
		return
	}
	os.RemoveAll(m.stagingDir(s.ID))
	database.DB.Exec("DELETE FROM upload_session_chunks WHERE session_id = ?", s.ID)
	log.Printf("[Upload] session=%.8s finalized as file %d (%d bytes, %d retries)", s.ID, fileID, s.TotalSize, s.RetryCount)
	if m.OnPublished != nil {
		m.OnPublished(fileID, s.Owner, deref(s.MimeType))
	}
}

// publish makes the file visible in the browser. Everything happens in one
// transaction, so the file appears only when all parts are recorded, and a
// repeated call after a crash finds the session already finalized.
func (m *Manager) publish(s *Session, parts []Part) (int64, string, error) {
	database.EnsureFoldersExist(s.Path, s.Owner)
	unlock := database.AcquireFileInsertLock(s.Owner, s.Path)
	defer unlock()

	tx, err := database.DB.Beginx()
	if err != nil {
		return 0, "", err
	}
	defer tx.Rollback()

	var cur Session
	if err := tx.Get(&cur, "SELECT * FROM upload_sessions WHERE id = ?", s.ID); err != nil {
		return 0, "", err
	}
	if cur.State == StateFinalized && cur.FileID != nil {
		return *cur.FileID, deref(cur.FinalName), nil
	}
	if cur.State != StateFinalizing {
		return 0, "", errors.New("session is no longer finalizing")
	}

	var oldID int
	var oldThumb *string
	var orphans []int
	if s.Overwrite == 1 {
		var old struct {
			ID    int     `db:"id"`
			Thumb *string `db:"thumb_path"`
		}
		if err := tx.Get(&old, "SELECT id, thumb_path FROM files WHERE path = ? AND filename = ? AND is_folder = FALSE AND owner = ? AND deleted_at IS NULL", s.Path, s.Filename, s.Owner); err == nil {
			oldID, oldThumb = old.ID, old.Thumb
			orphans, _ = database.GetOrphanedMessages([]int{oldID})
			if _, err := tx.Exec("DELETE FROM file_parts WHERE file_id = ?", oldID); err != nil {
				return 0, "", err
			}
			if _, err := tx.Exec("DELETE FROM files WHERE id = ?", oldID); err != nil {
				return 0, "", err
			}
		}
	}

	name := database.GetUniqueFilename(tx, s.Path, s.Filename, false, 0, s.Owner)
	mime := deref(s.MimeType)
	if mime == "" {
		mime = "application/octet-stream"
	}
	fileID, err := database.InsertAndGetID(tx,
		"INSERT INTO files (filename, path, size, mime_type, is_folder, owner, message_id) VALUES (?, ?, ?, ?, FALSE, ?, ?)",
		name, s.Path, s.TotalSize, mime, s.Owner, *parts[0].MessageID)
	if err != nil {
		return 0, "", err
	}
	if len(parts) > 1 {
		for _, p := range parts {
			if _, err := tx.Exec("INSERT INTO file_parts (file_id, message_id, part_index, size) VALUES (?, ?, ?, ?)", fileID, *p.MessageID, p.Index, p.Size); err != nil {
				return 0, "", err
			}
		}
	}
	res, err := tx.Exec("UPDATE upload_sessions SET state = ?, file_id = ?, final_name = ?, failure_reason = NULL, updated_at = ? WHERE id = ? AND state = ?",
		StateFinalized, fileID, name, now(), s.ID, StateFinalizing)
	if err != nil {
		return 0, "", err
	}
	if af, _ := res.RowsAffected(); af == 0 {
		return 0, "", errors.New("session changed during publish")
	}
	if err := tx.Commit(); err != nil {
		return 0, "", err
	}

	if len(orphans) > 0 {
		go m.bridge.DeleteMessages(context.Background(), orphans)
	}
	if oldThumb != nil {
		var c int
		if database.RODB.Get(&c, "SELECT COUNT(*) FROM files WHERE thumb_path = ?", *oldThumb) == nil && c == 0 {
			os.Remove(*oldThumb)
		}
	}
	return fileID, name, nil
}

// fail marks the session failed and discards its temporary data.
func (m *Manager) fail(id, reason string) {
	log.Printf("[Upload] session=%.8s failed: %s", id, reason)
	if changed, err := setState(id, StateFailed, reason); err != nil || !changed {
		return
	}
	m.discard(id)
}

func clip(s string) string {
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
