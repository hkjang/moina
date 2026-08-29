CREATE TABLE IF NOT EXISTS users (
    id text PRIMARY KEY,
    username text NOT NULL,
    display_name text NOT NULL,
    email text NOT NULL DEFAULT '',
    bio text NOT NULL DEFAULT '',
    avatar_id text NOT NULL DEFAULT '',
    password_hash text NOT NULL DEFAULT '',
    account_type text NOT NULL DEFAULT 'human' CHECK (account_type IN ('human','agent','service')),
    provider text NOT NULL DEFAULT 'local' CHECK (provider IN ('local','oidc','system')),
    roles text[] NOT NULL DEFAULT ARRAY['member']::text[],
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS users_username_lower_uidx ON users(lower(username));
CREATE INDEX IF NOT EXISTS users_active_idx ON users(active, created_at DESC);

CREATE TABLE IF NOT EXISTS oidc_identities (
    issuer text NOT NULL,
    subject text NOT NULL,
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    last_login_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (issuer, subject),
    UNIQUE (user_id, issuer)
);

CREATE TABLE IF NOT EXISTS sessions (
    id text PRIMARY KEY,
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    csrf_hash text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sessions_expires_idx ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS roles (
    name text PRIMARY KEY,
    description text NOT NULL DEFAULT '',
    system boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS role_permissions (
    role_name text NOT NULL REFERENCES roles(name) ON DELETE CASCADE,
    permission text NOT NULL,
    PRIMARY KEY(role_name, permission)
);
INSERT INTO roles(name,description,system) VALUES
    ('super_admin','전체 서비스 관리',true),
    ('admin','서비스 운영 관리',true),
    ('team_lead','검토 및 승인',true),
    ('moderator','신고 및 콘텐츠 운영',true),
    ('member','일반 사용자',true)
ON CONFLICT(name) DO NOTHING;
INSERT INTO role_permissions(role_name,permission) VALUES
    ('super_admin','*'),
    ('admin','admin:access'),('admin','users:manage'),('admin','posts:manage'),('admin','settings:manage'),('admin','roles:manage'),('admin','keys:manage'),('admin','audit:read'),('admin','moderation:manage'),('admin','approvals:review'),('admin','ai:use'),('admin','posts:write'),('admin','posts:read'),('admin','social:write'),('admin','mcp:use'),
    ('team_lead','approvals:review'),('team_lead','posts:read'),('team_lead','ai:use'),('team_lead','mcp:use'),
    ('moderator','moderation:manage'),('moderator','posts:manage'),('moderator','posts:read'),('moderator','ai:use'),
    ('member','posts:read'),('member','posts:write'),('member','social:write'),('member','ai:use'),('member','mcp:use')
ON CONFLICT(role_name,permission) DO NOTHING;

CREATE TABLE IF NOT EXISTS settings (
    key text PRIMARY KEY,
    payload bytea NOT NULL,
    sensitive boolean NOT NULL DEFAULT false,
    revision bigint NOT NULL DEFAULT 1,
    updated_by text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_preferences (
    user_id text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS api_keys (
    id text PRIMARY KEY,
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name text NOT NULL,
    prefix text NOT NULL,
    token_hash text NOT NULL UNIQUE,
    permissions text[] NOT NULL DEFAULT ARRAY[]::text[],
    version integer NOT NULL DEFAULT 1,
    expires_at timestamptz,
    revoked_at timestamptz,
    last_used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    rotated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS api_keys_user_idx ON api_keys(user_id, created_at DESC);
CREATE TABLE IF NOT EXISTS api_key_rotations (
    id text PRIMARY KEY,
    key_id text NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    version integer NOT NULL,
    prefix text NOT NULL,
    rotated_by text NOT NULL,
    reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS audit_events (
    id text PRIMARY KEY,
    actor_id text NOT NULL DEFAULT '',
    action text NOT NULL,
    target_type text NOT NULL DEFAULT '',
    target_id text NOT NULL DEFAULT '',
    success boolean NOT NULL,
    ip text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    detail jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS audit_events_created_idx ON audit_events(created_at DESC);

CREATE TABLE IF NOT EXISTS follows (
    follower_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    followee_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(follower_id,followee_id),
    CHECK(follower_id<>followee_id)
);
CREATE INDEX IF NOT EXISTS follows_followee_idx ON follows(followee_id,created_at DESC);
CREATE TABLE IF NOT EXISTS blocks (
    blocker_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    blocked_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(blocker_id,blocked_id),
    CHECK(blocker_id<>blocked_id)
);
CREATE TABLE IF NOT EXISTS mutes (
    muter_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    muted_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(muter_id,muted_id),
    CHECK(muter_id<>muted_id)
);

CREATE TABLE IF NOT EXISTS topics (
    id text PRIMARY KEY,
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS topics_search_idx ON topics USING gin(to_tsvector('simple', name || ' ' || description));
CREATE TABLE IF NOT EXISTS user_topic_follows (
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    topic_id text NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    weight smallint NOT NULL DEFAULT 50 CHECK(weight BETWEEN 0 AND 100),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(user_id,topic_id)
);

CREATE TABLE IF NOT EXISTS moims (
    id text PRIMARY KEY,
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    owner_id text NOT NULL REFERENCES users(id),
    visibility text NOT NULL DEFAULT 'public' CHECK(visibility IN ('public','private')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS moim_members (
    moim_id text NOT NULL REFERENCES moims(id) ON DELETE CASCADE,
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role text NOT NULL DEFAULT 'member' CHECK(role IN ('owner','moderator','member')),
    joined_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(moim_id,user_id)
);

CREATE TABLE IF NOT EXISTS media_assets (
    id text PRIMARY KEY,
    owner_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    filename text NOT NULL,
    mime_type text NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 text NOT NULL,
    width integer NOT NULL DEFAULT 0,
    height integer NOT NULL DEFAULT 0,
    data bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS media_owner_idx ON media_assets(owner_id,created_at DESC);

CREATE TABLE IF NOT EXISTS posts (
    id text PRIMARY KEY,
    author_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    content text NOT NULL DEFAULT '',
    kind text NOT NULL DEFAULT 'moin' CHECK(kind IN ('moin','echo','quote','remoin')),
    visibility text NOT NULL DEFAULT 'public' CHECK(visibility IN ('public','followers','moim')),
    status text NOT NULL DEFAULT 'published' CHECK(status IN ('draft','pending_approval','published','rejected','deleted')),
    reply_to_id text REFERENCES posts(id),
    quote_post_id text REFERENCES posts(id),
    remoin_post_id text REFERENCES posts(id),
    moim_id text REFERENCES moims(id),
    approval_required boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    deleted_at timestamptz
);
CREATE INDEX IF NOT EXISTS posts_feed_idx ON posts(status,published_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS posts_author_idx ON posts(author_id,status,created_at DESC);
CREATE INDEX IF NOT EXISTS posts_reply_idx ON posts(reply_to_id,status,created_at);
CREATE INDEX IF NOT EXISTS posts_search_idx ON posts USING gin(to_tsvector('simple', content));
CREATE TABLE IF NOT EXISTS post_media (
    post_id text NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    media_id text NOT NULL REFERENCES media_assets(id) ON DELETE CASCADE,
    position smallint NOT NULL DEFAULT 0,
    PRIMARY KEY(post_id,media_id)
);
CREATE TABLE IF NOT EXISTS post_topics (
    post_id text NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    topic_id text NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    source text NOT NULL DEFAULT 'hashtag' CHECK(source IN ('hashtag','ai','manual')),
    confidence real NOT NULL DEFAULT 1 CHECK(confidence BETWEEN 0 AND 1),
    PRIMARY KEY(post_id,topic_id)
);
CREATE INDEX IF NOT EXISTS post_topics_topic_idx ON post_topics(topic_id,post_id);
CREATE TABLE IF NOT EXISTS reactions (
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    post_id text NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK(kind IN ('like','useful','insight','question','verify')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(user_id,post_id,kind)
);
CREATE INDEX IF NOT EXISTS reactions_post_idx ON reactions(post_id,kind);
CREATE TABLE IF NOT EXISTS bookmarks (
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    post_id text NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(user_id,post_id)
);

CREATE TABLE IF NOT EXISTS notifications (
    id text PRIMARY KEY,
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    actor_id text REFERENCES users(id) ON DELETE SET NULL,
    type text NOT NULL,
    target_id text NOT NULL DEFAULT '',
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    read_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS notifications_user_idx ON notifications(user_id,read_at,created_at DESC);

CREATE TABLE IF NOT EXISTS approval_requests (
    id text PRIMARY KEY,
    action text NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    requester_id text NOT NULL REFERENCES users(id),
    status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','rejected','cancelled')),
    snapshot bytea NOT NULL,
    reviewer_id text REFERENCES users(id),
    comment text NOT NULL DEFAULT '',
    requested_at timestamptz NOT NULL DEFAULT now(),
    reviewed_at timestamptz
);
CREATE INDEX IF NOT EXISTS approvals_status_idx ON approval_requests(status,requested_at DESC);

CREATE TABLE IF NOT EXISTS reports (
    id text PRIMARY KEY,
    reporter_id text NOT NULL REFERENCES users(id),
    target_type text NOT NULL CHECK(target_type IN ('post','user','moim')),
    target_id text NOT NULL,
    reason text NOT NULL,
    detail text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'open' CHECK(status IN ('open','reviewing','resolved','dismissed')),
    resolution text NOT NULL DEFAULT '',
    moderator_id text REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz
);
CREATE INDEX IF NOT EXISTS reports_status_idx ON reports(status,created_at DESC);

CREATE TABLE IF NOT EXISTS moderation_actions (
    id text PRIMARY KEY,
    report_id text REFERENCES reports(id),
    moderator_id text NOT NULL REFERENCES users(id),
    action text NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ai_usage_events (
    id text PRIMARY KEY,
    user_id text NOT NULL REFERENCES users(id),
    model text NOT NULL,
    api_style text NOT NULL,
    max_tokens integer NOT NULL,
    success boolean NOT NULL,
    latency_ms bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ai_usage_created_idx ON ai_usage_events(created_at DESC);

CREATE TABLE IF NOT EXISTS outbox_events (
    id text PRIMARY KEY,
    event_type text NOT NULL,
    aggregate_id text NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz
);
CREATE INDEX IF NOT EXISTS outbox_pending_idx ON outbox_events(created_at) WHERE delivered_at IS NULL;
