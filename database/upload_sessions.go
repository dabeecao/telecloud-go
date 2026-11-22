package database

import "fmt"

// EnsureUploadSessionTables creates the tables backing the resumable upload
// session API. HTTP transport chunks (upload_session_chunks) and Telegram
// storage parts (upload_session_parts) are deliberately separate tables: a
// storage part is assembled from many chunks and is the unit stored in Telegram.
//
// Timestamps are epoch seconds so the same SQL works on every driver.
func EnsureUploadSessionTables() error {
	idType, blobType, tail := "TEXT", "BLOB", ""
	switch {
	case IsMySQL():
		idType, blobType = "VARCHAR(64)", "BLOB"
		tail = " ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci"
	case IsPostgres():
		blobType = "BYTEA"
	}

	stmts := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS upload_sessions (
			id %[1]s PRIMARY KEY,
			owner VARCHAR(191) NOT NULL,
			filename TEXT NOT NULL,
			path TEXT NOT NULL,
			mime_type TEXT,
			overwrite INTEGER NOT NULL DEFAULT 0,
			total_size BIGINT NOT NULL,
			chunk_size BIGINT NOT NULL,
			total_chunks BIGINT NOT NULL,
			part_size BIGINT NOT NULL,
			total_parts BIGINT NOT NULL,
			state VARCHAR(16) NOT NULL,
			expected_sha256 VARCHAR(64),
			hash_state %[2]s,
			hashed_parts BIGINT NOT NULL DEFAULT 0,
			file_id BIGINT,
			final_name TEXT,
			failure_reason TEXT,
			retry_count BIGINT NOT NULL DEFAULT 0,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL,
			last_chunk_at BIGINT NOT NULL DEFAULT 0
		)%[3]s`, idType, blobType, tail),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS upload_session_chunks (
			session_id %[1]s NOT NULL,
			chunk_index BIGINT NOT NULL,
			offset_bytes BIGINT NOT NULL,
			size BIGINT NOT NULL,
			sha256 VARCHAR(64) NOT NULL,
			created_at BIGINT NOT NULL,
			PRIMARY KEY (session_id, chunk_index)
		)%[2]s`, idType, tail),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS upload_session_parts (
			session_id %[1]s NOT NULL,
			part_index BIGINT NOT NULL,
			offset_bytes BIGINT NOT NULL,
			size BIGINT NOT NULL,
			state VARCHAR(16) NOT NULL,
			message_id BIGINT,
			attempts BIGINT NOT NULL DEFAULT 0,
			last_error TEXT,
			updated_at BIGINT NOT NULL,
			PRIMARY KEY (session_id, part_index)
		)%[2]s`, idType, tail),
	}
	for _, s := range stmts {
		if _, err := DB.Exec(s); err != nil {
			return fmt.Errorf("failed to create upload session tables: %v", err)
		}
	}
	// Index creation is best effort: IF NOT EXISTS is not portable to MySQL.
	if IsMySQL() {
		if err := createIndexMySQL("idx_upload_sessions_owner", "upload_sessions", "owner", false); err != nil {
			return err
		}
		return createIndexMySQL("idx_upload_sessions_updated", "upload_sessions", "updated_at", false)
	}
	DB.Exec("CREATE INDEX IF NOT EXISTS idx_upload_sessions_owner ON upload_sessions(owner)")
	DB.Exec("CREATE INDEX IF NOT EXISTS idx_upload_sessions_updated ON upload_sessions(updated_at)")
	return nil
}
