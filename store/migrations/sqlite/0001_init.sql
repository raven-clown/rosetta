CREATE TABLE revision (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    value INTEGER NOT NULL
);
INSERT INTO revision (id, value) VALUES (1, 0);

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    org_id TEXT NOT NULL DEFAULT 'default',
    agent TEXT NOT NULL,
    session_key TEXT NOT NULL,
    session_id TEXT NOT NULL,
    caller TEXT NOT NULL DEFAULT '',
    messages INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    version INTEGER NOT NULL DEFAULT 1,
    UNIQUE (org_id, agent, session_key)
);
CREATE INDEX sessions_updated ON sessions (org_id, updated_at, id);
CREATE INDEX sessions_expires ON sessions (expires_at);

CREATE TABLE messages (
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    org_id TEXT NOT NULL DEFAULT 'default',
    seq INTEGER NOT NULL,
    role TEXT NOT NULL,
    parts TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (session_id, seq)
);

CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    org_id TEXT NOT NULL DEFAULT 'default',
    agent TEXT NOT NULL,
    protocol TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    caller TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    started_at INTEGER NOT NULL,
    ended_at INTEGER NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    request_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX runs_started ON runs (org_id, started_at, id);

CREATE TABLE agents (
    org_id TEXT NOT NULL DEFAULT 'default',
    name TEXT NOT NULL,
    spec TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    updated_by TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (org_id, name)
);

CREATE TABLE api_keys (
    id TEXT PRIMARY KEY,
    org_id TEXT NOT NULL DEFAULT 'default',
    name TEXT NOT NULL,
    role TEXT NOT NULL,
    prefix TEXT NOT NULL,
    hash TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    created_by TEXT NOT NULL DEFAULT '',
    revoked_at INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX api_keys_created ON api_keys (org_id, created_at, id);

CREATE TABLE audit (
    id TEXT PRIMARY KEY,
    org_id TEXT NOT NULL DEFAULT 'default',
    at INTEGER NOT NULL,
    actor TEXT NOT NULL,
    action TEXT NOT NULL,
    target TEXT NOT NULL,
    details TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_at ON audit (org_id, at, id);
