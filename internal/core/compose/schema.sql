CREATE TABLE compose_projects (
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

CREATE INDEX compose_projects_node_name ON compose_projects(node_id, project_name);

CREATE TABLE compose_operations (
    operation_id TEXT PRIMARY KEY,
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    project_key TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_digest BLOB NOT NULL CHECK (length(request_digest) = 32),
    request_json TEXT NOT NULL,
    action TEXT NOT NULL CHECK (action IN ('validate', 'up', 'start', 'stop', 'restart', 'down')),
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'timed_out', 'unknown')),
    error_code TEXT NOT NULL DEFAULT '',
    verified INTEGER NOT NULL DEFAULT 0 CHECK (verified IN (0, 1)),
    actor_id INTEGER,
    remote_addr TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (node_id, idempotency_key),
    FOREIGN KEY (node_id, project_key) REFERENCES compose_projects(node_id, project_key)
);

CREATE INDEX compose_operations_node_created ON compose_operations(node_id, created_at DESC, operation_id DESC);

CREATE TABLE compose_operation_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    operation_id TEXT NOT NULL REFERENCES compose_operations(operation_id) ON DELETE CASCADE,
    event TEXT NOT NULL,
    from_status TEXT,
    to_status TEXT NOT NULL,
    actor_id INTEGER,
    remote_addr TEXT NOT NULL,
    occurred_at INTEGER NOT NULL
);

CREATE INDEX compose_operation_events_operation ON compose_operation_events(operation_id, id);
