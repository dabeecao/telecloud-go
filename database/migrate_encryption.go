package database

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"

	"telecloud/utils"
)

// encryptionSchemaVersion is the latest version of the encryption-at-rest layout
// that this build understands. Bump when the encrypted format changes.
const encryptionSchemaVersion = 1

// ensureSchemaVersionTable creates the schema_version table (id is the namespace,
// version the integer level) if it doesn't already exist on the active driver.
func ensureSchemaVersionTable() error {
	var ddl string
	switch {
	case IsMySQL():
		ddl = "CREATE TABLE IF NOT EXISTS schema_version (id VARCHAR(64) PRIMARY KEY, version INT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
	case IsPostgres():
		ddl = "CREATE TABLE IF NOT EXISTS schema_version (id TEXT PRIMARY KEY, version INT NOT NULL)"
	default:
		ddl = "CREATE TABLE IF NOT EXISTS schema_version (id TEXT PRIMARY KEY, version INTEGER NOT NULL)"
	}
	_, err := DB.Exec(ddl)
	return err
}

func getSchemaVersion(id string) (int, error) {
	var v int
	err := RODB.Get(&v, "SELECT version FROM schema_version WHERE id = ?", id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v, err
}

func setSchemaVersion(id string, version int) error {
	query := "INSERT INTO schema_version (id, version) VALUES (?, ?) ON DUPLICATE KEY UPDATE version = VALUES(version)"
	if IsPostgres() {
		query = "INSERT INTO schema_version (id, version) VALUES (?, ?) ON CONFLICT(id) DO UPDATE SET version = EXCLUDED.version"
	} else if !IsMySQL() {
		query = "INSERT INTO schema_version (id, version) VALUES (?, ?) ON CONFLICT(id) DO UPDATE SET version = excluded.version"
	}
	_, err := DB.Exec(query, id, version)
	return err
}

// MigrateEncryptV1 re-encrypts existing plaintext rows in tg_sessions and the
// sensitive settings list under the master key. It runs at most once per
// install; subsequent calls are no-ops.
func MigrateEncryptV1() error {
	if err := ensureSchemaVersionTable(); err != nil {
		return fmt.Errorf("schema_version table: %w", err)
	}
	current, err := getSchemaVersion("encryption")
	if err != nil {
		return fmt.Errorf("read schema_version: %w", err)
	}
	if current >= encryptionSchemaVersion {
		return nil
	}

	// Pre-flight: ensure the master key is loadable. Without it, encryption is
	// impossible and we would corrupt data.
	if _, err := utils.LoadMasterKey(); err != nil {
		return err
	}

	if err := reEncryptTGSessions(); err != nil {
		return fmt.Errorf("re-encrypt tg_sessions: %w", err)
	}
	if err := reEncryptSensitiveSettings(); err != nil {
		return fmt.Errorf("re-encrypt settings: %w", err)
	}

	if err := setSchemaVersion("encryption", encryptionSchemaVersion); err != nil {
		return fmt.Errorf("mark schema_version: %w", err)
	}
	return nil
}

func reEncryptTGSessions() error {
	rows, err := RODB.Query("SELECT session_id, data FROM tg_sessions")
	if err != nil {
		return err
	}
	defer rows.Close()

	type row struct {
		id   string
		data []byte
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.data); err != nil {
			return err
		}
		if len(r.data) == 0 {
			continue
		}
		if _, err := utils.DecryptAEAD(r.data); err == nil {
			continue
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range pending {
		enc, err := utils.EncryptAEAD(r.data)
		if err != nil {
			return fmt.Errorf("encrypt session %s: %w", r.id, err)
		}
		if _, err := DB.Exec("UPDATE tg_sessions SET data = ?, updated_at = CURRENT_TIMESTAMP WHERE session_id = ?", enc, r.id); err != nil {
			return fmt.Errorf("update session %s: %w", r.id, err)
		}
		log.Printf("[migration] Encrypted tg_sessions row %s", r.id)
	}
	return nil
}

func reEncryptSensitiveSettings() error {
	for _, key := range SensitiveSettingKeys() {
		raw := GetSettingRaw(key)
		if raw == "" || utils.IsEncryptedString(raw) {
			continue
		}
		// Force re-write through SetSetting so the value gets encrypted via the
		// auto-encrypt path. We bypass the no-op short-circuit because the value
		// IS plaintext.
		if err := SetSetting(key, raw); err != nil {
			return fmt.Errorf("encrypt setting %s: %w", key, err)
		}
		log.Printf("[migration] Encrypted setting %q", key)
	}
	return nil
}

// IsEncryptionMigrationNeeded is a cheap pre-boot probe so the runner can decide
// whether to print the manual-dump warning before InitDB blocks.
func IsEncryptionMigrationNeeded() bool {
	v, err := getSchemaVersion("encryption")
	if err != nil {
		return true
	}
	return v < encryptionSchemaVersion
}

// migrationSafeSplit splits a SQL blob on ';' for batched DDL. Exposed so
// other migrations can reuse the same logic. Currently unused outside this
// file but kept for future migrations.
func migrationSafeSplit(blob string) []string {
	parts := strings.Split(blob, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}
