CREATE TABLE file_write_task_events_v12_backup AS SELECT * FROM file_write_task_events;

CREATE TABLE file_write_tasks_v13 (
    task_id TEXT PRIMARY KEY,
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    idempotency_key TEXT,
    request_digest TEXT,
    operation TEXT NOT NULL CHECK (operation IN ('mkdir','rename','delete','save_text','upload')),
    target_path TEXT NOT NULL CHECK (length(target_path) BETWEEN 1 AND 4096),
    new_path TEXT NOT NULL DEFAULT '' CHECK (length(new_path) <= 4096),
    status TEXT NOT NULL CHECK (status IN ('queued','running','succeeded','failed','canceled','unknown')),
    result_code TEXT NOT NULL DEFAULT '',
    created_at_ns INTEGER NOT NULL,
    updated_at_ns INTEGER NOT NULL,
    dispatch_started_at_ns INTEGER,
    started_at_ns INTEGER,
    finished_at_ns INTEGER,
    CHECK ((operation = 'rename' AND length(new_path) > 0) OR (operation <> 'rename' AND new_path = '')),
    CHECK ((idempotency_key IS NULL AND request_digest IS NULL) OR (length(idempotency_key) BETWEEN 1 AND 200 AND length(request_digest)=64)),
    CHECK ((status IN ('queued','running') AND finished_at_ns IS NULL) OR (status IN ('succeeded','failed','canceled','unknown') AND finished_at_ns IS NOT NULL))
);

INSERT INTO file_write_tasks_v13(task_id,node_id,idempotency_key,request_digest,operation,target_path,new_path,status,result_code,created_at_ns,updated_at_ns,dispatch_started_at_ns,started_at_ns,finished_at_ns)
SELECT task_id,node_id,NULL,NULL,operation,target_path,new_path,status,result_code,created_at_ns,updated_at_ns,dispatch_started_at_ns,started_at_ns,finished_at_ns FROM file_write_tasks;

DROP TABLE file_write_task_events;
DROP TABLE file_write_tasks;
ALTER TABLE file_write_tasks_v13 RENAME TO file_write_tasks;

CREATE UNIQUE INDEX file_write_tasks_idempotency ON file_write_tasks(node_id,idempotency_key);
CREATE INDEX file_write_tasks_node_created ON file_write_tasks(node_id,created_at_ns DESC,task_id DESC);
CREATE INDEX file_write_tasks_retention ON file_write_tasks(status,finished_at_ns,task_id);

CREATE TABLE file_write_task_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
    task_id TEXT NOT NULL REFERENCES file_write_tasks(task_id) ON DELETE CASCADE,
    event TEXT NOT NULL CHECK (event IN ('accepted','dispatch_started','agent_started','task_resolved','task_recovered')),
    from_status TEXT CHECK (from_status IS NULL OR from_status IN ('queued','running','succeeded','failed','canceled','unknown')),
    to_status TEXT CHECK (to_status IS NULL OR to_status IN ('queued','running','succeeded','failed','canceled','unknown')),
    actor_id INTEGER,
    occurred_at_ns INTEGER NOT NULL,
    remote_addr TEXT NOT NULL DEFAULT 'unknown'
);

INSERT INTO file_write_task_events(id,node_id,task_id,event,from_status,to_status,actor_id,occurred_at_ns,remote_addr)
SELECT id,node_id,task_id,event,from_status,to_status,actor_id,occurred_at_ns,remote_addr FROM file_write_task_events_v12_backup;

DROP TABLE file_write_task_events_v12_backup;
CREATE INDEX file_write_task_events_task ON file_write_task_events(node_id,task_id,id);
