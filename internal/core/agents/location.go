package agents

import "context"

// EnrollmentIP uses the authenticated Agent's enrollment audit record, never an
// administrator's browser address or the address of the current list request.
func (r *Repository) EnrollmentIP(ctx context.Context, agentID string) string {
	if agentID == "" {
		return ""
	}
	var ip string
	_ = r.db.QueryRowContext(ctx, `SELECT remote_addr FROM audit_entries WHERE action='agent_enrollment_consume' AND outcome='succeeded' AND target_kind='agent' AND target_id=? ORDER BY id DESC LIMIT 1`, agentID).Scan(&ip)
	return ip
}
