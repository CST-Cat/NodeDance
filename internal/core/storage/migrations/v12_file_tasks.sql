CREATE TABLE file_write_tasks (
    task_id TEXT PRIMARY KEY,
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    operation TEXT NOT NULL CHECK (operation IN ('mkdir','rename','delete','save_text','upload')),
    target_path TEXT NOT NULL CHECK (length(target_path) BETWEEN 1 AND 4096),
    new_path TEXT NOT NULL DEFAULT '' CHECK (length(new_path) <= 4096),
    status TEXT NOT NULL CHECK (status IN ('queued','running','succeeded','failed','unknown')),
    result_code TEXT NOT NULL DEFAULT '',
    created_at_ns INTEGER NOT NULL,
    updated_at_ns INTEGER NOT NULL,
    dispatch_started_at_ns INTEGER,
    started_at_ns INTEGER,
    finished_at_ns INTEGER,
    CHECK ((operation = 'rename' AND length(new_path) > 0) OR (operation <> 'rename' AND new_path = '')),
    CHECK ((status IN ('queued','running') AND finished_at_ns IS NULL) OR (status IN ('succeeded','failed','unknown') AND finished_at_ns IS NOT NULL))
);

CREATE INDEX file_write_tasks_node_created ON file_write_tasks(node_id, created_at_ns DESC, task_id DESC);
CREATE INDEX file_write_tasks_retention ON file_write_tasks(status, finished_at_ns, task_id);

CREATE TABLE file_write_task_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    task_id TEXT NOT NULL REFERENCES file_write_tasks(task_id) ON DELETE CASCADE,
    event TEXT NOT NULL CHECK (event IN ('accepted','dispatch_started','agent_started','task_resolved','task_recovered')),
    from_status TEXT CHECK (from_status IS NULL OR from_status IN ('queued','running','succeeded','failed','unknown')),
    to_status TEXT CHECK (to_status IS NULL OR to_status IN ('queued','running','succeeded','failed','unknown')),
    actor_id INTEGER,
    occurred_at_ns INTEGER NOT NULL,
    remote_addr TEXT NOT NULL DEFAULT 'unknown'
);

CREATE INDEX file_write_task_events_task ON file_write_task_events(node_id, task_id, id);
