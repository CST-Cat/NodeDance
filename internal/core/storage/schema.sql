CREATE TABLE IF NOT EXISTS admin_user (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	password_hash BLOB NOT NULL,
	password_salt BLOB NOT NULL,
	password_version TEXT NOT NULL,
	display_name TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS init_credential (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	digest BLOB NOT NULL,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS browser_sessions (
	id TEXT PRIMARY KEY,
	token_digest BLOB NOT NULL UNIQUE,
	csrf_digest BLOB NOT NULL,
	created_at INTEGER NOT NULL,
	last_seen_at INTEGER NOT NULL,
	user_agent TEXT NOT NULL,
	remote_addr TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS browser_sessions_last_seen ON browser_sessions(last_seen_at);

CREATE TABLE IF NOT EXISTS appearance (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	display_name TEXT NOT NULL,
	theme TEXT NOT NULL,
	background_color TEXT NOT NULL,
	avatar_png BLOB,
	background_png BLOB,
	updated_at INTEGER NOT NULL
);

INSERT OR IGNORE INTO appearance(id, display_name, theme, background_color, updated_at)
	VALUES(1, 'NodeDance Admin', 'system', '#101827', 0);

CREATE TABLE IF NOT EXISTS audit_entries (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	occurred_at INTEGER NOT NULL,
	action TEXT NOT NULL,
	outcome TEXT NOT NULL,
	actor_id INTEGER,
	remote_addr TEXT NOT NULL,
	target_kind TEXT,
	target_id TEXT
);

CREATE INDEX IF NOT EXISTS audit_entries_target ON audit_entries(target_kind, target_id);
CREATE INDEX IF NOT EXISTS audit_entries_retention ON audit_entries(occurred_at, id);

CREATE TABLE IF NOT EXISTS nodes (
	id TEXT PRIMARY KEY,
	display_name TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('pending', 'online', 'offline', 'revoked')),
	connection_generation INTEGER NOT NULL DEFAULT 0 CHECK (connection_generation >= 0),
	last_seen_at INTEGER,
	heartbeat_sequence INTEGER NOT NULL DEFAULT 0 CHECK (heartbeat_sequence >= 0),
	protocol_version INTEGER,
	agent_version TEXT,
	capabilities_json TEXT NOT NULL DEFAULT '[]',
	runtime_os TEXT,
	runtime_architecture TEXT,
	effective_uid INTEGER,
	effective_gid INTEGER,
	supplementary_groups_json TEXT NOT NULL DEFAULT '[]',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS nodes_status_seen ON nodes(status, last_seen_at);

CREATE TABLE IF NOT EXISTS agent_devices (
	id TEXT PRIMARY KEY,
	node_id TEXT NOT NULL UNIQUE REFERENCES nodes(id),
	credential_digest BLOB NOT NULL UNIQUE CHECK (length(credential_digest) = 32),
	pending_credential_digest BLOB UNIQUE CHECK (pending_credential_digest IS NULL OR length(pending_credential_digest) = 32),
	pending_rotation_id TEXT,
	rotation_requested_id TEXT,
	revoked_at INTEGER,
	created_at INTEGER NOT NULL,
	CHECK (pending_credential_digest IS NULL OR pending_credential_digest != credential_digest),
	CHECK ((pending_credential_digest IS NULL) = (pending_rotation_id IS NULL))
);

CREATE INDEX IF NOT EXISTS agent_devices_node ON agent_devices(node_id);

CREATE TABLE IF NOT EXISTS agent_credential_verifiers (
	digest BLOB PRIMARY KEY CHECK (length(digest) = 32),
	agent_id TEXT NOT NULL REFERENCES agent_devices(id) ON DELETE CASCADE,
	state TEXT NOT NULL CHECK (state IN ('active', 'pending')),
	UNIQUE(agent_id, state)
);

CREATE INDEX IF NOT EXISTS agent_credential_verifiers_agent ON agent_credential_verifiers(agent_id);

CREATE TABLE IF NOT EXISTS agent_enrollments (
	token_digest BLOB PRIMARY KEY CHECK (length(token_digest) = 32),
	node_id TEXT NOT NULL REFERENCES nodes(id),
	request_id TEXT,
	expires_at INTEGER NOT NULL,
	consumed_at INTEGER,
	created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS agent_enrollments_node ON agent_enrollments(node_id);

CREATE TABLE IF NOT EXISTS docker_node_state (
	node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
	agent_id TEXT NOT NULL,
	generation INTEGER NOT NULL CHECK (generation > 0),
	health_json TEXT,
	health_received_at INTEGER NOT NULL,
	stale_reason TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS docker_containers (
	node_id TEXT NOT NULL REFERENCES docker_node_state(node_id) ON DELETE CASCADE,
	container_id TEXT NOT NULL,
	record_json TEXT NOT NULL,
	PRIMARY KEY(node_id, container_id)
);

CREATE INDEX IF NOT EXISTS docker_containers_node ON docker_containers(node_id, container_id);

CREATE TABLE IF NOT EXISTS dashboard_preferences (
	node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
	target_kind TEXT NOT NULL CHECK (target_kind IN ('node', 'container', 'compose_service')),
	identity_key TEXT NOT NULL,
	alias TEXT NOT NULL DEFAULT '',
	icon TEXT NOT NULL DEFAULT '',
	notes TEXT NOT NULL DEFAULT '',
	service_url TEXT NOT NULL DEFAULT '',
	group_name TEXT NOT NULL DEFAULT '' CHECK (length(group_name) <= 128),
	sort_order INTEGER NOT NULL DEFAULT 0,
	visible INTEGER NOT NULL DEFAULT 1 CHECK (visible IN (0, 1)),
	pinned INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0, 1)),
	updated_at INTEGER NOT NULL,
	PRIMARY KEY(node_id, target_kind, identity_key)
);

CREATE TABLE IF NOT EXISTS node_connection_preferences (
	node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
	ssh_host TEXT NOT NULL DEFAULT '',
	ssh_port INTEGER NOT NULL DEFAULT 22 CHECK (ssh_port BETWEEN 1 AND 65535),
	ssh_user TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS dashboard_settings (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	view_mode TEXT NOT NULL CHECK (view_mode IN ('monitor', 'manage')),
	group_by TEXT NOT NULL CHECK (group_by IN ('node', 'compose', 'state', 'none')),
	sort_by TEXT NOT NULL CHECK (sort_by IN ('custom', 'name', 'state')),
	node_group_by TEXT NOT NULL DEFAULT 'status' CHECK (node_group_by IN ('group', 'status', 'none')),
	node_sort_by TEXT NOT NULL DEFAULT 'custom' CHECK (node_sort_by IN ('custom', 'name', 'status')),
	featured_limit INTEGER NOT NULL CHECK (featured_limit BETWEEN 1 AND 20),
	custom_fields_json TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);

INSERT OR IGNORE INTO dashboard_settings(id, view_mode, group_by, sort_by, node_group_by, node_sort_by, featured_limit, custom_fields_json, updated_at)
	VALUES(1, 'monitor', 'node', 'custom', 'status', 'custom', 4, '["state","ports","health"]', 0);

CREATE TABLE IF NOT EXISTS metrics_minute (
	node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
	metric_key TEXT NOT NULL,
	bucket_at INTEGER NOT NULL,
	sample_count INTEGER NOT NULL CHECK (sample_count > 0),
	sample_sum REAL NOT NULL,
	minimum REAL NOT NULL,
	maximum REAL NOT NULL,
	PRIMARY KEY(node_id, metric_key, bucket_at)
);

CREATE INDEX IF NOT EXISTS metrics_minute_retention ON metrics_minute(bucket_at);

CREATE TABLE IF NOT EXISTS metrics_hour (
	node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
	metric_key TEXT NOT NULL,
	bucket_at INTEGER NOT NULL,
	sample_count INTEGER NOT NULL CHECK (sample_count > 0),
	sample_sum REAL NOT NULL,
	minimum REAL NOT NULL,
	maximum REAL NOT NULL,
	PRIMARY KEY(node_id, metric_key, bucket_at)
);

CREATE INDEX IF NOT EXISTS metrics_hour_retention ON metrics_hour(bucket_at);

CREATE TABLE IF NOT EXISTS compose_projects (
	node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
	project_key TEXT NOT NULL,
	project_name TEXT NOT NULL,
	working_directory TEXT NOT NULL,
	config_files_json TEXT NOT NULL,
	config_available INTEGER NOT NULL CHECK (config_available IN (0, 1)),
	discovered_at INTEGER NOT NULL,
	last_seen_at INTEGER NOT NULL,
	PRIMARY KEY (node_id, project_key)
);

CREATE INDEX IF NOT EXISTS compose_projects_node_name ON compose_projects(node_id, project_name);

CREATE TABLE IF NOT EXISTS service_probes (
	id TEXT PRIMARY KEY,
	node_id TEXT NOT NULL REFERENCES nodes(id),
	name TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('http', 'https', 'tcp')),
	target TEXT NOT NULL,
	expected_http_status INTEGER,
	interval_seconds INTEGER NOT NULL CHECK (interval_seconds BETWEEN 10 AND 86400),
	timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 30 AND timeout_seconds < interval_seconds),
	enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
	revision INTEGER NOT NULL CHECK (revision > 0),
	status TEXT NOT NULL CHECK (status IN ('unknown', 'healthy', 'unhealthy')),
	last_error_code TEXT,
	consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
	consecutive_successes INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_successes >= 0),
	last_checked_at INTEGER,
	next_due_at INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	deleted_at INTEGER,
	CHECK ((kind = 'tcp' AND expected_http_status IS NULL) OR (kind IN ('http', 'https') AND expected_http_status BETWEEN 100 AND 599))
);

CREATE INDEX IF NOT EXISTS service_probes_due ON service_probes(enabled, deleted_at, next_due_at);
CREATE INDEX IF NOT EXISTS service_probes_node ON service_probes(node_id, deleted_at, id);

CREATE TABLE IF NOT EXISTS service_probe_runs (
	run_id TEXT PRIMARY KEY,
	probe_id TEXT NOT NULL REFERENCES service_probes(id),
	node_id TEXT NOT NULL REFERENCES nodes(id),
	probe_revision INTEGER NOT NULL,
	generation INTEGER NOT NULL DEFAULT 0,
	kind TEXT NOT NULL CHECK (kind IN ('http', 'https', 'tcp')),
	expected_http_status INTEGER,
	status TEXT NOT NULL CHECK (status IN ('pending', 'healthy', 'unhealthy', 'unknown')),
	checked_at INTEGER NOT NULL,
	completed_at INTEGER,
	latency_ms INTEGER,
	http_status INTEGER,
	error_code TEXT,
	timeout_seconds INTEGER NOT NULL,
	CHECK ((kind = 'tcp' AND expected_http_status IS NULL) OR (kind IN ('http', 'https') AND expected_http_status BETWEEN 100 AND 599))
);

CREATE INDEX IF NOT EXISTS service_probe_runs_probe_time ON service_probe_runs(probe_id, checked_at DESC, run_id DESC);
CREATE INDEX IF NOT EXISTS service_probe_runs_pending ON service_probe_runs(status, checked_at);
CREATE INDEX IF NOT EXISTS service_probe_runs_retention ON service_probe_runs(status, completed_at, checked_at);

CREATE TABLE IF NOT EXISTS alert_rules (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('node_offline', 'cpu', 'memory', 'disk', 'docker_unavailable', 'container_state', 'probe_state')),
	node_id TEXT NOT NULL REFERENCES nodes(id),
	subject_id TEXT NOT NULL DEFAULT '',
	severity TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
	threshold REAL,
	duration_seconds INTEGER NOT NULL CHECK (duration_seconds BETWEEN 0 AND 86400),
	cooldown_seconds INTEGER NOT NULL CHECK (cooldown_seconds BETWEEN 0 AND 86400),
	expected_state TEXT NOT NULL DEFAULT '',
	channel_ids_json TEXT NOT NULL DEFAULT '[]',
	enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
	revision INTEGER NOT NULL CHECK (revision > 0),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	deleted_at INTEGER,
	CHECK ((kind IN ('cpu', 'memory', 'disk') AND threshold BETWEEN 0 AND 100) OR (kind NOT IN ('cpu', 'memory', 'disk') AND threshold IS NULL))
);

CREATE INDEX IF NOT EXISTS alert_rules_node ON alert_rules(node_id, enabled, deleted_at, kind);

CREATE TABLE IF NOT EXISTS alert_rule_state (
	rule_id TEXT NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
	subject_id TEXT NOT NULL,
	condition_since INTEGER NOT NULL,
	last_sample_at INTEGER NOT NULL,
	last_value REAL,
	last_state TEXT NOT NULL,
	PRIMARY KEY(rule_id, subject_id)
);

CREATE TABLE IF NOT EXISTS alerts (
	id TEXT PRIMARY KEY,
	fingerprint TEXT NOT NULL,
	rule_id TEXT NOT NULL,
	rule_name TEXT NOT NULL,
	node_id TEXT NOT NULL,
	node_name TEXT NOT NULL,
	subject_id TEXT NOT NULL,
	severity TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
	status TEXT NOT NULL CHECK (status IN ('active', 'resolved')),
	message TEXT NOT NULL,
	current_value REAL,
	channel_ids_json TEXT NOT NULL DEFAULT '[]',
	first_seen_at INTEGER NOT NULL,
	last_seen_at INTEGER NOT NULL,
	resolved_at INTEGER,
	acknowledged_at INTEGER,
	acknowledged_by TEXT NOT NULL DEFAULT '',
	acknowledged_note TEXT NOT NULL DEFAULT '',
	silenced_until INTEGER,
	last_notified_at INTEGER,
	suppression_reason TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS alerts_active_fingerprint ON alerts(fingerprint) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS alerts_active_recent ON alerts(status, last_seen_at DESC);
CREATE INDEX IF NOT EXISTS alerts_node_history ON alerts(node_id, first_seen_at DESC);
CREATE INDEX IF NOT EXISTS alerts_retention ON alerts(status, resolved_at, id);

CREATE TABLE IF NOT EXISTS alert_events (
	id TEXT PRIMARY KEY,
	alert_id TEXT NOT NULL REFERENCES alerts(id),
	kind TEXT NOT NULL CHECK (kind IN ('firing', 'reminder', 'recovered', 'acknowledged', 'silenced', 'unsilenced', 'suppressed')),
	occurred_at INTEGER NOT NULL,
	message TEXT NOT NULL,
	details_json TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS alert_events_time ON alert_events(occurred_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS alert_events_alert ON alert_events(alert_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS alert_events_retention ON alert_events(occurred_at, alert_id, id);

CREATE TABLE IF NOT EXISTS alert_channels (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('webhook', 'smtp')),
	config_json TEXT NOT NULL,
	secret_ciphertext BLOB,
	enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
	revision INTEGER NOT NULL CHECK (revision > 0),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	deleted_at INTEGER
);

CREATE TABLE IF NOT EXISTS alert_deliveries (
	id TEXT PRIMARY KEY,
	alert_id TEXT,
	event_id TEXT,
	channel_id TEXT NOT NULL,
	channel_name TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('webhook', 'smtp')),
	payload_json TEXT NOT NULL,
	test_send INTEGER NOT NULL DEFAULT 0 CHECK (test_send IN (0, 1)),
	status TEXT NOT NULL CHECK (status IN ('queued', 'sending', 'retry', 'sent', 'failed', 'suppressed')),
	attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 3),
	max_attempts INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 3),
	next_attempt_at INTEGER,
	claimed_at INTEGER,
	delivered_at INTEGER,
	http_status INTEGER,
	last_error TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS alert_deliveries_due ON alert_deliveries(status, next_attempt_at, created_at);
CREATE INDEX IF NOT EXISTS alert_deliveries_history ON alert_deliveries(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS alert_deliveries_retention ON alert_deliveries(status, created_at, alert_id, event_id);

CREATE TABLE IF NOT EXISTS alert_windows (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL CHECK (kind IN ('silence', 'maintenance')),
	scope_type TEXT NOT NULL CHECK (scope_type IN ('global', 'node', 'rule')),
	scope_id TEXT,
	starts_at INTEGER NOT NULL,
	ends_at INTEGER NOT NULL CHECK (ends_at > starts_at),
	reason TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	disabled_at INTEGER,
	CHECK ((scope_type = 'global' AND scope_id IS NULL) OR (scope_type IN ('node', 'rule') AND scope_id IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS alert_windows_active ON alert_windows(starts_at, ends_at, kind, scope_type, scope_id);
CREATE INDEX IF NOT EXISTS alert_windows_retention ON alert_windows(ends_at, disabled_at);
