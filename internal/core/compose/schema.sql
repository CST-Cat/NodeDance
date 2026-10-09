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
