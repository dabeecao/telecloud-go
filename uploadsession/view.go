package uploadsession

import (
	"telecloud/config"
	"time"

	"telecloud/database"
)

// View is the client-facing status of a session.
type View struct {
	ID              string `json:"id"`
	State           string `json:"state"`
	Filename        string `json:"filename"`
	TotalSize       int64  `json:"total_size"`
	Path            string `json:"path"`
	ExpectedSHA256  string `json:"expected_sha256,omitempty"`
	ChunkSize       int64  `json:"chunk_size"`
	TotalChunks     int64  `json:"total_chunks"`
	PartSize        int64  `json:"part_size"`
	TotalParts      int64  `json:"total_parts"`
	BytesReceived   int64  `json:"bytes_received"`
	ConfirmedChunks int64  `json:"confirmed_chunks"`
	// ConfirmedRanges lists confirmed chunk indexes as inclusive [first,last]
	// pairs, so a resuming client skips them without a huge payload.
	ConfirmedRanges [][2]int64 `json:"confirmed_ranges"`
	PartsStored     int64      `json:"parts_stored"`
	PartsFailed     int64      `json:"parts_failed"`
	ThroughputBps   float64    `json:"throughput_bytes_per_sec"`
	RetryCount      int64      `json:"retry_count"`
	FailureReason   string     `json:"failure_reason,omitempty"`
	FileID          *int64     `json:"file_id,omitempty"`
	FinalName       string     `json:"final_name,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
}

// Status returns the current state of a session owned by owner.
func (m *Manager) Status(owner, id string) (*View, error) {
	if _, err := m.owned(id, owner); err != nil {
		return nil, err
	}
	return m.view(id, owner)
}

func (m *Manager) view(id, owner string) (*View, error) {
	s, err := m.owned(id, owner)
	if err != nil {
		return nil, err
	}
	n, bytes, err := countChunks(id)
	if err != nil {
		return nil, err
	}
	v := &View{
		ID: s.ID, State: s.State, Filename: s.Filename, Path: s.Path, ExpectedSHA256: deref(s.ExpectedSHA256), TotalSize: s.TotalSize, ChunkSize: s.ChunkSize,
		TotalChunks: s.TotalChunks, PartSize: s.PartSize, TotalParts: s.TotalParts,
		BytesReceived: bytes, ConfirmedChunks: n, RetryCount: s.RetryCount,
		FailureReason: deref(s.FailureReason), FileID: s.FileID, FinalName: deref(s.FinalName),
		CreatedAt: time.Unix(s.CreatedAt, 0).UTC(), UpdatedAt: time.Unix(s.UpdatedAt, 0).UTC(),
		ExpiresAt:       time.Unix(s.UpdatedAt, 0).Add(m.cfg.ChunkedUpload.SessionTTL).UTC(),
		ConfirmedRanges: [][2]int64{},
	}
	if s.LastChunkAt > s.CreatedAt {
		v.ThroughputBps = float64(bytes) / float64(s.LastChunkAt-s.CreatedAt)
	}
	if s.State != StateFinalized {
		idx, err := chunkIndexes(id)
		if err != nil {
			return nil, err
		}
		for _, i := range idx {
			if k := len(v.ConfirmedRanges); k > 0 && v.ConfirmedRanges[k-1][1] == i-1 {
				v.ConfirmedRanges[k-1][1] = i
			} else {
				v.ConfirmedRanges = append(v.ConfirmedRanges, [2]int64{i, i})
			}
		}
	}
	var pc []struct {
		State string `db:"state"`
		N     int64  `db:"n"`
	}
	if err := database.RODB.Select(&pc, "SELECT state, COUNT(*) AS n FROM upload_session_parts WHERE session_id = ? GROUP BY state", id); err == nil {
		for _, c := range pc {
			switch c.State {
			case partUploaded:
				v.PartsStored = c.N
			case partFailed:
				v.PartsFailed = c.N
			}
		}
	}
	if s.State == StateFinalized {
		v.PartsStored = s.TotalParts
		v.ConfirmedChunks = s.TotalChunks
		v.BytesReceived = s.TotalSize
	}
	return v, nil
}

// List returns the caller's unfinished sessions so a client that lost its
// session ID (for example after a crash) can find and resume them.
func (m *Manager) List(owner string) ([]*View, error) {
	var ids []string
	if err := database.RODB.Select(&ids, "SELECT id FROM upload_sessions WHERE owner = ? AND state IN (?, ?) ORDER BY created_at DESC LIMIT 100", owner, StateOpen, StateFinalizing); err != nil {
		return nil, err
	}
	out := make([]*View, 0, len(ids))
	for _, id := range ids {
		if v, err := m.view(id, owner); err == nil {
			out = append(out, v)
		}
	}
	return out, nil
}

// Config returns the effective client-facing settings.
type ClientConfig struct {
	Enabled      bool  `json:"enabled"`
	ThresholdB   int64 `json:"threshold_bytes"`
	ChunkSizeB   int64 `json:"chunk_size_bytes"`
	MaxChunkB    int64 `json:"max_chunk_size_bytes"`
	MaxParallel  int   `json:"max_parallel_chunks"`
	SessionTTLHr int   `json:"session_ttl_hours"`
	// Guidance for auto-sizing: chunk_size_bytes is the ceiling; a client
	// should measure its upload speed (POST /api/uploads/probe) and choose
	// speed * target_chunk_seconds / parallel_streams, within
	// [min_chunk_size_bytes, chunk_size_bytes].
	MinChunkB      int64 `json:"min_chunk_size_bytes"`
	TargetChunkSec int   `json:"target_chunk_seconds"`
}

// TargetChunkSeconds is how long one chunk request should take. It keeps a
// chunk well under the ~100 s origin timeout of Cloudflare Tunnel.
const TargetChunkSeconds = 40

func (m *Manager) ClientConfig() ClientConfig {
	cu := m.cfg.ChunkedUpload
	return ClientConfig{
		Enabled: cu.Enabled, ThresholdB: cu.Threshold, ChunkSizeB: cu.ChunkSize,
		MaxChunkB: 50 << 20, MinChunkB: config.MinChunkedUploadChunkSize, TargetChunkSec: TargetChunkSeconds, MaxParallel: cu.MaxParallel, SessionTTLHr: int(cu.SessionTTL / time.Hour),
	}
}
