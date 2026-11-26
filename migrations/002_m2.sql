BEGIN;

ALTER TABLE groups
    ADD COLUMN leader_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL;

CREATE TABLE migration_revisions (
    id BIGSERIAL PRIMARY KEY,
    migration_id BIGINT NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
    revision_no INT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('draft', 'submitted')),
    sql_up TEXT NOT NULL,
    sql_down TEXT,
    irreversible BOOLEAN NOT NULL DEFAULT FALSE,
    irreversible_reason TEXT,
    mode TEXT NOT NULL DEFAULT 'normal' CHECK (mode IN ('normal', 'batch')),
    batch_table TEXT,
    batch_column TEXT,
    batch_size INT,
    created_by BIGINT NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    submitted_at TIMESTAMPTZ,
    UNIQUE (migration_id, revision_no),
    CHECK (irreversible OR sql_down IS NOT NULL),
    CHECK (NOT irreversible OR irreversible_reason IS NOT NULL),
    CHECK (mode = 'normal' OR (batch_table IS NOT NULL AND batch_column IS NOT NULL AND batch_size > 0))
);

ALTER TABLE migrations
    ALTER COLUMN sql DROP NOT NULL,
    ALTER COLUMN status DROP NOT NULL,
    ADD COLUMN is_applied BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN current_revision_id BIGINT REFERENCES migration_revisions(id);

CREATE TABLE change_requests (
    id BIGSERIAL PRIMARY KEY,
    db_connection_id BIGINT NOT NULL REFERENCES db_connections(id) ON DELETE CASCADE,
    migration_id BIGINT NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
    revision_id BIGINT NOT NULL REFERENCES migration_revisions(id),
    action TEXT NOT NULL CHECK (action IN ('apply', 'rollback', 'forward', 'resume', 'revert')),
    target_execution_id UUID,
    status TEXT NOT NULL CHECK (status IN ('pending_review', 'approved', 'rejected', 'queued',
        'running', 'success', 'failed', 'partial', 'uncertain')),
    validation JSONB,
    requested_by BIGINT NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX change_requests_db_status_idx ON change_requests (db_connection_id, status);

CREATE TABLE approvals (
    change_request_id BIGINT NOT NULL REFERENCES change_requests(id) ON DELETE CASCADE,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    approver_id BIGINT NOT NULL REFERENCES users(id),
    decision TEXT NOT NULL CHECK (decision IN ('approve', 'reject')),
    comment TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (change_request_id, group_id)
);

CREATE TABLE jobs (
    id BIGSERIAL PRIMARY KEY,
    change_request_id BIGINT NOT NULL UNIQUE REFERENCES change_requests(id) ON DELETE CASCADE,
    db_connection_id BIGINT NOT NULL REFERENCES db_connections(id) ON DELETE CASCADE,
    execution_id UUID NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'success', 'failed', 'partial', 'uncertain')),
    attempt INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 5,
    run_after TIMESTAMPTZ NOT NULL DEFAULT now(),
    worker_id TEXT,
    lease_until TIMESTAMPTZ,
    batches_committed INT NOT NULL DEFAULT 0,
    rows_committed BIGINT NOT NULL DEFAULT 0,
    last_error TEXT,
    last_sqlstate TEXT,
    trace_context JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);
CREATE INDEX jobs_claim_idx ON jobs (status, run_after);
CREATE INDEX jobs_db_idx ON jobs (db_connection_id, created_at DESC);

CREATE TABLE database_locks (
    db_connection_id BIGINT PRIMARY KEY REFERENCES db_connections(id) ON DELETE CASCADE,
    job_id BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    worker_id TEXT NOT NULL,
    acquired_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_until TIMESTAMPTZ NOT NULL
);

CREATE TABLE migration_history (
    id BIGSERIAL PRIMARY KEY,
    execution_id UUID NOT NULL,
    attempt INT NOT NULL CHECK (attempt >= 1),
    db_connection_id BIGINT NOT NULL REFERENCES db_connections(id) ON DELETE CASCADE,
    migration_id BIGINT NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
    revision_id BIGINT NOT NULL REFERENCES migration_revisions(id),
    version INT,
    action TEXT NOT NULL CHECK (action IN ('apply', 'rollback', 'forward', 'revert')),
    status TEXT NOT NULL CHECK (status IN ('success', 'failed', 'partial')),
    reverts_execution_id UUID,
    executed_sql TEXT NOT NULL,
    mode TEXT NOT NULL,
    batches INT,
    rows_affected BIGINT,
    error_message TEXT,
    sqlstate TEXT,
    requested_by BIGINT NOT NULL REFERENCES users(id),
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ NOT NULL,
    UNIQUE (execution_id, attempt),
    CHECK (status <> 'success' OR action = 'revert' OR version IS NOT NULL)
);
CREATE UNIQUE INDEX migration_history_one_success_idx ON migration_history (execution_id) WHERE status = 'success';
CREATE INDEX migration_history_db_idx ON migration_history (db_connection_id, finished_at, id);

CREATE FUNCTION reject_history_change() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' AND pg_trigger_depth() > 1 THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'migration_history tidak boleh diubah atau dihapus';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER migration_history_immutable
    BEFORE UPDATE OR DELETE ON migration_history
    FOR EACH ROW EXECUTE FUNCTION reject_history_change();

INSERT INTO migration_revisions (
    migration_id, revision_no, status, sql_up, irreversible, irreversible_reason,
    mode, created_by, created_at, submitted_at
)
SELECT id, 1,
       CASE WHEN status = 'draft' THEN 'draft' ELSE 'submitted' END,
       sql, TRUE, 'Migration M1 tanpa SQL down', 'normal', created_by, created_at,
       CASE WHEN status = 'draft' THEN NULL ELSE executed_at END
FROM migrations;

UPDATE migrations m
SET current_revision_id = r.id,
    is_applied = (m.status = 'success')
FROM migration_revisions r
WHERE r.migration_id = m.id AND r.revision_no = 1;

INSERT INTO migration_history (
    execution_id, attempt, db_connection_id, migration_id, revision_id, version, action, status,
    executed_sql, mode, error_message, requested_by, started_at, finished_at
)
SELECT gen_random_uuid(), 1, m.db_connection_id, m.id, m.current_revision_id, m.version, 'apply', m.status,
       m.sql, 'normal', m.error_message, COALESCE(m.executed_by, m.created_by), m.executed_at, m.executed_at
FROM migrations m
WHERE m.status IN ('success', 'failed');

COMMIT;