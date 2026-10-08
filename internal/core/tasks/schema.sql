CREATE TABLE core_tasks (
    task_id TEXT PRIMARY KEY,
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    idempotency_key TEXT NOT NULL,
    request_digest BLOB NOT NULL CHECK (length(request_digest) = 32),
    accepted_generation INTEGER NOT NULL CHECK (accepted_generation >= 0),
    target_id TEXT NOT NULL,
    resource_key TEXT NOT NULL,
    action TEXT NOT NULL,
    intent_json TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued','running','succeeded','failed','timed_out','canceled','unknown')),
    delivery_state TEXT NOT NULL DEFAULT 'ready' CHECK (delivery_state IN ('ready','sent','needs_reconciliation','done')),
    dispatch_journal_id TEXT,
    reconciliation_required INTEGER NOT NULL DEFAULT 0 CHECK (reconciliation_required IN (0,1)),
    created_at_ns INTEGER NOT NULL,
    updated_at_ns INTEGER NOT NULL,
    started_at_ns INTEGER,
    finished_at_ns INTEGER,
    execution_attempted INTEGER NOT NULL DEFAULT 0 CHECK (execution_attempted IN (0,1)),
    execution_completed INTEGER NOT NULL DEFAULT 0 CHECK (execution_completed IN (0,1)),
    failure_confirmed INTEGER NOT NULL DEFAULT 0 CHECK (failure_confirmed IN (0,1)),
    postcondition_verified INTEGER NOT NULL DEFAULT 0 CHECK (postcondition_verified IN (0,1)),
    process_terminated INTEGER NOT NULL DEFAULT 0 CHECK (process_terminated IN (0,1)),
    actual_result_confirmed INTEGER NOT NULL DEFAULT 0 CHECK (actual_result_confirmed IN (0,1)),
    cancellation_confirmed INTEGER NOT NULL DEFAULT 0 CHECK (cancellation_confirmed IN (0,1)),
    delivery_committed INTEGER NOT NULL DEFAULT 0 CHECK (delivery_committed IN (0,1)),
    progress_phase TEXT NOT NULL DEFAULT 'accepted',
    progress_completed INTEGER NOT NULL DEFAULT 0 CHECK (progress_completed >= 0),
    progress_total INTEGER NOT NULL DEFAULT 0 CHECK (progress_total >= 0),
    result_code TEXT NOT NULL DEFAULT '',
    observed_state TEXT NOT NULL DEFAULT '',
    resource_revision TEXT NOT NULL DEFAULT '',
    UNIQUE(node_id, idempotency_key)
);

CREATE INDEX core_tasks_node_created ON core_tasks(node_id, created_at_ns DESC, task_id DESC);
CREATE INDEX core_tasks_node_status ON core_tasks(node_id, status, created_at_ns);

CREATE TABLE core_task_resource_claims (
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    resource_key TEXT NOT NULL,
    task_id TEXT NOT NULL UNIQUE REFERENCES core_tasks(task_id) ON DELETE RESTRICT,
    PRIMARY KEY(node_id, resource_key)
);

CREATE TABLE core_task_audit_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    task_id TEXT REFERENCES core_tasks(task_id) ON DELETE RESTRICT,
    event TEXT NOT NULL CHECK (event IN ('accepted','delivery_claimed','agent_status','agent_progress','journal_changed','task_resolved','journal_accepted','journal_replaced')),
    from_status TEXT CHECK (from_status IS NULL OR from_status IN ('queued','running','succeeded','failed','timed_out','canceled','unknown')),
    to_status TEXT CHECK (to_status IS NULL OR to_status IN ('queued','running','succeeded','failed','timed_out','canceled','unknown')),
    actor_id INTEGER,
    occurred_at_ns INTEGER NOT NULL,
    remote_addr TEXT NOT NULL DEFAULT 'unknown'
);

CREATE INDEX core_task_audit_task ON core_task_audit_events(node_id, task_id, id);

CREATE TABLE core_task_agent_state (
    node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE RESTRICT,
    journal_id TEXT NOT NULL CHECK (length(journal_id) = 64),
    pending_journal_id TEXT CHECK (pending_journal_id IS NULL OR length(pending_journal_id) = 64),
    review_required INTEGER NOT NULL DEFAULT 0 CHECK (review_required IN (0,1)),
    updated_at_ns INTEGER NOT NULL,
    CHECK ((pending_journal_id IS NULL) = (review_required = 0))
);

CREATE TRIGGER core_tasks_keep_unresolved
BEFORE DELETE ON core_tasks
WHEN OLD.status IN ('queued','running','unknown')
BEGIN
    SELECT RAISE(ABORT, 'unresolved tasks must be retained');
END;
