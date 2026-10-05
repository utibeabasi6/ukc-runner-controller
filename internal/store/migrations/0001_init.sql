CREATE TABLE runners (
    name                TEXT PRIMARY KEY,
    scale_set           TEXT NOT NULL,
    github_runner_id    INTEGER NOT NULL DEFAULT 0,
    instance_uuid       TEXT NOT NULL DEFAULT '',
    image               TEXT NOT NULL,
    vcpus               INTEGER NOT NULL,
    memory_mb           INTEGER NOT NULL,
    disk_mb             INTEGER NOT NULL,
    state               TEXT NOT NULL,
    error               TEXT NOT NULL DEFAULT '',
    exit_code           INTEGER,
    stop_reason         TEXT NOT NULL DEFAULT '',
    boot_time_ms        INTEGER,
    log_tail            TEXT NOT NULL DEFAULT '',
    created_at          DATETIME NOT NULL,
    job_started_at      DATETIME,
    finished_at         DATETIME,
    instance_deleted_at DATETIME
);

CREATE INDEX runners_scale_set_state ON runners (scale_set, state);
CREATE INDEX runners_created_at ON runners (created_at);

CREATE TABLE jobs (
    request_id      INTEGER PRIMARY KEY,
    scale_set       TEXT NOT NULL,
    job_id          TEXT NOT NULL DEFAULT '',
    owner           TEXT NOT NULL DEFAULT '',
    repository      TEXT NOT NULL DEFAULT '',
    workflow_ref    TEXT NOT NULL DEFAULT '',
    display_name    TEXT NOT NULL DEFAULT '',
    event_name      TEXT NOT NULL DEFAULT '',
    workflow_run_id INTEGER NOT NULL DEFAULT 0,
    runner_name     TEXT NOT NULL DEFAULT '',
    result          TEXT NOT NULL DEFAULT '',
    queued_at       DATETIME NOT NULL,
    assigned_at     DATETIME,
    started_at      DATETIME,
    completed_at    DATETIME
);

CREATE INDEX jobs_queued_at ON jobs (queued_at);
CREATE INDEX jobs_runner_name ON jobs (runner_name);

CREATE TABLE events (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    at        DATETIME NOT NULL,
    scale_set TEXT NOT NULL DEFAULT '',
    level     TEXT NOT NULL,
    message   TEXT NOT NULL
);

CREATE INDEX events_at ON events (at);
