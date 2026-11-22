package database

import (
	"strings"
	"time"
)

// AbandonedFileRowAge is how old a placeholder `files` row must be before it is
// considered abandoned rather than in-flight.
//
// Upload paths insert the `files` row before the Telegram parts exist and fill
// in `message_id` only once the first part is stored (see
// tgclient.ProcessCompleteUpload and uploadsession.publish). If the process is
// killed between those two points the row survives with `message_id` NULL and
// nothing ever fills it in: it is invisible in every listing (they filter on
// `message_id IS NOT NULL`) but it still occupies the filename, so the next
// upload of the same name is renamed to "name (1)" and the collision dialog
// reports a file that cannot be opened.
//
// The window has to clear the longest legitimate gap between the insert and the
// first stored part, which is not a few seconds: the row is inserted *before*
// the upload semaphore is taken, so a large file can sit queued for a long time
// on a busy server. 24 h matches the retention used by the TempDir sweeper and
// is far beyond any real queue wait.
const AbandonedFileRowAge = 24 * time.Hour

// CleanupAbandonedFileRows deletes placeholder `files` rows left behind by a
// process that died before storing the file's first Telegram part, and returns
// how many were removed.
//
// Only rows that provably hold no Telegram content are touched:
//   - `message_id IS NULL` — no stored part is referenced, so there is nothing
//     to delete from Telegram (RULE 2 is not in play: the orphaned parts of a
//     half-finished multi-part upload were never recorded in the database, so
//     they could not be resolved from here either way).
//   - no `file_parts` rows — defensive: a row that does have parts is never a
//     placeholder, whatever its `message_id` says.
//   - `is_folder = FALSE` — folders legitimately have no `message_id`.
//   - `deleted_at IS NULL` — the trash has its own retention policy.
//
// The candidates are selected first and then deleted by primary key. A
// correlated `DELETE ... WHERE NOT EXISTS (SELECT ... FROM file_parts ...)`
// would be rejected by MySQL (error 1093: can't specify target table for update
// in FROM clause is avoided, but the read-then-delete form is the pattern the
// rest of this package already uses and it keeps the statement ANSI-only).
func CleanupAbandonedFileRows(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)

	var ids []int64
	err := RODB.Select(&ids, `SELECT f.id FROM files f
		WHERE f.message_id IS NULL
		  AND f.is_folder = FALSE
		  AND f.deleted_at IS NULL
		  AND f.created_at < ?
		  AND NOT EXISTS (SELECT 1 FROM file_parts p WHERE p.file_id = f.id)`, cutoff)
	if err != nil || len(ids) == 0 {
		return 0, err
	}

	// Delete in chunks to keep the placeholder list bounded on PostgreSQL's
	// parameter limit and to avoid one long write lock on SQLite.
	const chunk = 200
	var removed int64
	for start := 0; start < len(ids); start += chunk {
		end := start + chunk
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]interface{}, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		res, err := DB.Exec("DELETE FROM files WHERE id IN ("+placeholders+")", args...)
		if err != nil {
			return removed, err
		}
		n, _ := res.RowsAffected()
		removed += n
	}
	return removed, nil
}
