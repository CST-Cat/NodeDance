CREATE TABLE compose_editor_operations (
    operation_id TEXT PRIMARY KEY,
    node_id TEXT NOT NULL,
    project_key TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_digest BLOB NOT NULL CHECK (length(request_digest) = 32),
    action TEXT NOT NULL CHECK (action IN ('edit_apply')),
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'unknown')),
    error_code TEXT NOT NULL DEFAULT '',
    verified INTEGER NOT NULL DEFAULT 0 CHECK (verified IN (0, 1)),
    summary_json TEXT NOT NULL DEFAULT '{}',
    actor_id INTEGER,
    remote_addr TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (node_id, idempotency_key),
    FOREIGN KEY (node_id, project_key) REFERENCES compose_projects(node_id, project_key) ON DELETE CASCADE
);

CREATE INDEX compose_editor_operations_node_created ON compose_editor_operations(node_id, created_at DESC, operation_id DESC);

CREATE TABLE compose_editor_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    operation_id TEXT NOT NULL REFERENCES compose_editor_operations(operation_id) ON DELETE CASCADE,
    event TEXT NOT NULL,
    from_status TEXT,
    to_status TEXT NOT NULL,
    occurred_at INTEGER NOT NULL
);

CREATE INDEX compose_editor_events_operation ON compose_editor_events(operation_id, id);
