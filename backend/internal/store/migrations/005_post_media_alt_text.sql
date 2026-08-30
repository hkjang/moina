-- Alternative text describes a media object in the context of a specific
-- Moin. Keep media_assets.alt_text as the upload/default value, then copy it
-- into every existing association so older posts retain their descriptions.
ALTER TABLE post_media
    ADD COLUMN IF NOT EXISTS alt_text text NOT NULL DEFAULT '';

UPDATE post_media AS association
SET alt_text = COALESCE(asset.alt_text, '')
FROM media_assets AS asset
WHERE asset.id = association.media_id
  AND (association.alt_text IS NULL OR association.alt_text = '');

UPDATE post_media SET alt_text = '' WHERE alt_text IS NULL;

ALTER TABLE post_media
    ALTER COLUMN alt_text SET DEFAULT '',
    ALTER COLUMN alt_text SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'post_media'::regclass
          AND conname = 'post_media_alt_text_length_check'
    ) THEN
        ALTER TABLE post_media
            ADD CONSTRAINT post_media_alt_text_length_check
            CHECK (char_length(alt_text) <= 500);
    END IF;
END
$$;
