CREATE TABLE jobs_by_job_id (
    job_id          TEXT PRIMARY KEY,
    request_id      INTEGER NOT NULL DEFAULT 0,
    scale_set       TEXT NOT NULL,
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

INSERT INTO jobs_by_job_id (job_id, request_id, scale_set, owner, repository, workflow_ref, display_name,
    event_name, workflow_run_id, runner_name, result, queued_at, assigned_at, started_at, completed_at)
SELECT job_id, request_id, scale_set, owner, repository, workflow_ref, display_name,
    event_name, workflow_run_id, runner_name, result, queued_at, assigned_at, started_at, completed_at
FROM jobs WHERE job_id != '';

DROP TABLE jobs;
ALTER TABLE jobs_by_job_id RENAME TO jobs;

CREATE INDEX jobs_queued_at ON jobs (queued_at);
CREATE INDEX jobs_runner_name ON jobs (runner_name);
