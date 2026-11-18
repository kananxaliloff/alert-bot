-- Users, roles hierarchy, permissions, tags, OTP challenges and audit log.

CREATE TABLE roles (
    id        BIGSERIAL PRIMARY KEY,
    name      TEXT NOT NULL UNIQUE,
    title     TEXT NOT NULL,
    parent_id BIGINT REFERENCES roles (id) ON DELETE SET NULL
);

CREATE TABLE role_permissions (
    role_id    BIGINT NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    permission TEXT NOT NULL,
    PRIMARY KEY (role_id, permission)
);

CREATE TABLE tags (
    id   BIGSERIAL PRIMARY KEY,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    UNIQUE (kind, name)
);

CREATE TABLE users (
    id          BIGSERIAL PRIMARY KEY,
    telegram_id BIGINT NOT NULL UNIQUE,
    email       TEXT,
    full_name   TEXT NOT NULL DEFAULT '',
    job_title   TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'unverified'
                CHECK (status IN ('unverified', 'pending', 'active', 'disabled')),
    role_id     BIGINT REFERENCES roles (id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    verified_at TIMESTAMPTZ,
    approved_at TIMESTAMPTZ,
    approved_by BIGINT
);

-- One Telegram account per work email. Emails are stored lower case.
CREATE UNIQUE INDEX users_email_key ON users (email) WHERE email IS NOT NULL;
CREATE INDEX users_status_idx ON users (status);

CREATE TABLE user_tags (
    user_id BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    tag_id  BIGINT NOT NULL REFERENCES tags (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, tag_id)
);

-- Every request for a code is a row, sent or not, so lookups can be rate limited.
CREATE TABLE otp_challenges (
    id          BIGSERIAL PRIMARY KEY,
    telegram_id BIGINT NOT NULL,
    email       TEXT NOT NULL,
    code_hash   BYTEA NOT NULL DEFAULT '\x',
    sent        BOOLEAN NOT NULL DEFAULT FALSE,
    expires_at  TIMESTAMPTZ NOT NULL,
    attempts    INT NOT NULL DEFAULT 0,
    consumed    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX otp_challenges_chat_idx ON otp_challenges (telegram_id, created_at DESC);
CREATE INDEX otp_challenges_email_idx ON otp_challenges (email, created_at DESC);

-- No foreign keys on purpose: audit rows must outlive the users they mention.
CREATE TABLE audit_log (
    id        BIGSERIAL PRIMARY KEY,
    at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_id  BIGINT,
    action    TEXT NOT NULL,
    target_id BIGINT,
    details   JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX audit_log_at_idx ON audit_log (at DESC);
