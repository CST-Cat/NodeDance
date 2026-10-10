CREATE TABLE IF NOT EXISTS journal_owner (
	id INTEGER PRIMARY KEY CHECK(id=1),
	node_id TEXT NOT NULL,
	journal_id TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS task_journal (
	task_id TEXT PRIMARY KEY,
	node_id TEXT NOT NULL,
	idempotency_key TEXT NOT NULL,
	request_digest BLOB NOT NULL CHECK(length(request_digest)=32),
	target_id TEXT NOT NULL,
	resource_key TEXT NOT NULL,
	action TEXT NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','timed_out','canceled','unknown')),
	created_at_ns INTEGER NOT NULL,
	updated_at_ns INTEGER NOT NULL,
	started_at_ns INTEGER,
	finished_at_ns INTEGER,
	execution_attempted INTEGER NOT NULL DEFAULT 0 CHECK(execution_attempted IN (0,1)),
	execution_completed INTEGER NOT NULL DEFAULT 0 CHECK(execution_completed IN (0,1)),
	failure_confirmed INTEGER NOT NULL DEFAULT 0 CHECK(failure_confirmed IN (0,1)),
	postcondition_verified INTEGER NOT NULL DEFAULT 0 CHECK(postcondition_verified IN (0,1)),
	process_terminated INTEGER NOT NULL DEFAULT 0 CHECK(process_terminated IN (0,1)),
	actual_result_confirmed INTEGER NOT NULL DEFAULT 0 CHECK(actual_result_confirmed IN (0,1)),
	cancellation_confirmed INTEGER NOT NULL DEFAULT 0 CHECK(cancellation_confirmed IN (0,1)),
	delivery_committed INTEGER NOT NULL DEFAULT 0 CHECK(delivery_committed IN (0,1)),
	progress_phase TEXT NOT NULL DEFAULT 'accepted',
	progress_completed INTEGER NOT NULL DEFAULT 0,
	progress_total INTEGER NOT NULL DEFAULT 0,
	result_code TEXT NOT NULL DEFAULT '',
	observed_state TEXT NOT NULL DEFAULT '',
	resource_revision TEXT NOT NULL DEFAULT '',
	execution_phase TEXT NOT NULL DEFAULT 'none' CHECK(execution_phase IN ('none','mutation_may_have_started','result_persisted')),
	baseline_verified INTEGER NOT NULL DEFAULT 0 CHECK(baseline_verified IN (0,1)),
	baseline_target_id TEXT NOT NULL DEFAULT '',
	baseline_action TEXT NOT NULL DEFAULT '',
	baseline_host_boot_id TEXT NOT NULL DEFAULT '',
	baseline_started_at TEXT NOT NULL DEFAULT '',
	baseline_restart_count INTEGER NOT NULL DEFAULT -1 CHECK(baseline_restart_count >= -1),
	baseline_running INTEGER NOT NULL DEFAULT 0 CHECK(baseline_running IN (0,1)),
	baseline_paused INTEGER NOT NULL DEFAULT 0 CHECK(baseline_paused IN (0,1)),
	baseline_restarting INTEGER NOT NULL DEFAULT 0 CHECK(baseline_restarting IN (0,1)),
	task_log BLOB NOT NULL DEFAULT X'',
	log_truncated INTEGER NOT NULL DEFAULT 0 CHECK(log_truncated IN (0,1)),
	UNIQUE(node_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS task_journal_resource_status ON task_journal(node_id, resource_key, status);

CREATE TABLE IF NOT EXISTS active_resource_claims (
	node_id TEXT NOT NULL,
	resource_key TEXT NOT NULL,
	task_id TEXT NOT NULL UNIQUE REFERENCES task_journal(task_id) ON DELETE CASCADE,
	PRIMARY KEY(node_id, resource_key)
);

CREATE TABLE IF NOT EXISTS compose_config_backups (
	project_key TEXT PRIMARY KEY,
	file_count INTEGER NOT NULL CHECK(file_count > 0 AND file_count <= 16),
	created_at_ns INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS compose_config_backup_files (
	project_key TEXT NOT NULL REFERENCES compose_config_backups(project_key) ON DELETE CASCADE,
	file_index INTEGER NOT NULL CHECK(file_index >= 0 AND file_index < 16),
	content BLOB NOT NULL,
	sha256 TEXT NOT NULL,
	PRIMARY KEY(project_key, file_index)
);
