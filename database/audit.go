package database

import (
	"log"
	"time"

	"github.com/gin-gonic/gin"
)

// Audit action constants. Keep them stable — external log analysis tooling
// may match on these strings.
const (
	AuditActionLoginSuccess    = "login_success"
	AuditActionLoginFail       = "login_fail"
	AuditActionLogout          = "logout"
	AuditActionPasswordChange  = "password_change"
	AuditActionAdminReset      = "admin_reset"
	AuditActionSetupComplete   = "setup_complete"
	AuditActionSetupConfig     = "setup_config"
	AuditActionWebDAVToggle    = "webdav_toggle"
	AuditActionUploadAPIToggle = "upload_api_toggle"
	AuditActionAPIKeyRotated   = "api_key_rotated"
	AuditActionAnalyticsToggle = "analytics_toggle"
)

const (
	AuditStatusOK     = "ok"
	AuditStatusDenied = "denied"
	AuditStatusError  = "error"
)

const auditRetention = 90 * 24 * time.Hour

// AuditEvent is the row written to audit_log. Use LogAudit / LogAuditFromCtx
// rather than INSERT-ing directly so the format stays consistent.
type AuditEvent struct {
	Timestamp time.Time
	Actor     string // username; "" if anonymous (e.g. failed login)
	Action    string
	Target    string // optional secondary identifier (target user, share token, setting key, ...)
	Status    string
	IP        string
	UserAgent string
}

// LogAudit persists an AuditEvent. Failures are logged but never propagated:
// auditing must never break the user-facing flow.
func LogAudit(e AuditEvent) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	if len(e.UserAgent) > 512 {
		e.UserAgent = e.UserAgent[:512]
	}
	_, err := DB.Exec(
		"INSERT INTO audit_log (ts, actor, action, target, status, ip, ua) VALUES (?, ?, ?, ?, ?, ?, ?)",
		e.Timestamp, e.Actor, e.Action, e.Target, e.Status, e.IP, e.UserAgent,
	)
	if err != nil {
		log.Printf("[Audit] failed to record %q for %q: %v", e.Action, e.Actor, err)
	}
}

// LogAuditFromCtx is a thin wrapper that pulls IP / UA from a gin.Context so
// handler call sites don't have to repeat the same three lines.
func LogAuditFromCtx(c *gin.Context, actor, action, target, status string) {
	ip := ""
	ua := ""
	if c != nil {
		ip = c.ClientIP()
		ua = c.Request.UserAgent()
	}
	LogAudit(AuditEvent{
		Actor:     actor,
		Action:    action,
		Target:    target,
		Status:    status,
		IP:        ip,
		UserAgent: ua,
	})
}

// CleanupExpiredAudit drops audit rows older than the retention horizon.
// Called by the periodic cleanup task next to session cleanup.
func CleanupExpiredAudit() int64 {
	threshold := time.Now().Add(-auditRetention)
	res, err := DB.Exec("DELETE FROM audit_log WHERE ts < ?", threshold)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}
