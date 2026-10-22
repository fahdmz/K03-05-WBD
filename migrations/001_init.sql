CREATE TABLE users (
    id BIGSERIAL PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    is_admin BOOLEAN NOT NULL DEFAULT FALSE,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    current_token_hash TEXT NOT NULL UNIQUE,
    previous_token_hash TEXT UNIQUE,
    previous_valid_until TIMESTAMPTZ,
    csrf_token TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_rotated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    absolute_expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ
);

CREATE TABLE groups (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT ''
);

CREATE TABLE group_members (
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);

CREATE TABLE db_connections (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    host TEXT NOT NULL,
    port INT NOT NULL DEFAULT 5432,
    dbname TEXT NOT NULL,
    username TEXT NOT NULL,
    password_encrypted BYTEA NOT NULL,
    sslmode TEXT NOT NULL DEFAULT 'disable',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE group_db_access (
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    db_connection_id BIGINT NOT NULL REFERENCES db_connections(id) ON DELETE CASCADE,
    level TEXT NOT NULL CHECK (level IN ('view', 'edit')),
    PRIMARY KEY (group_id, db_connection_id)
);

CREATE TABLE migrations (
    id BIGSERIAL PRIMARY KEY,
    db_connection_id BIGINT NOT NULL REFERENCES db_connections(id) ON DELETE CASCADE,
    version INT,
    title TEXT NOT NULL,
    sql TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('draft', 'success', 'failed')),
    created_by BIGINT NOT NULL REFERENCES users(id),
    executed_by BIGINT REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    executed_at TIMESTAMPTZ,
    error_message TEXT,
    UNIQUE (db_connection_id, version)
);