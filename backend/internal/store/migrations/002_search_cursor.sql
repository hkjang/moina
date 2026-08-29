-- Search relevance and keyset Flow indexes. pg_trgm ships with PostgreSQL and
-- remains fully offline; no external search service is required.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX IF NOT EXISTS users_username_trgm_idx
    ON users USING gin (lower(username) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS users_display_name_trgm_idx
    ON users USING gin (lower(display_name) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS users_bio_trgm_idx
    ON users USING gin (lower(bio) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS users_search_fts_idx
    ON users USING gin (to_tsvector('simple', username || ' ' || display_name || ' ' || bio));

CREATE INDEX IF NOT EXISTS topics_slug_trgm_idx
    ON topics USING gin (lower(slug) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS topics_name_trgm_idx
    ON topics USING gin (lower(name) gin_trgm_ops);

CREATE INDEX IF NOT EXISTS moims_slug_trgm_idx
    ON moims USING gin (lower(slug) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS moims_name_trgm_idx
    ON moims USING gin (lower(name) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS moims_description_trgm_idx
    ON moims USING gin (lower(description) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS moims_search_fts_idx
    ON moims USING gin (to_tsvector('simple', name || ' ' || description));

CREATE INDEX IF NOT EXISTS posts_content_trgm_idx
    ON posts USING gin (lower(content) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS posts_published_cursor_idx
    ON posts (published_at DESC, id DESC)
    WHERE status = 'published' AND published_at IS NOT NULL;

ALTER TABLE media_assets
    ADD COLUMN IF NOT EXISTS alt_text text NOT NULL DEFAULT '' CHECK (char_length(alt_text) <= 500);
