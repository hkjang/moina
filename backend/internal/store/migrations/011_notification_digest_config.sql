ALTER TABLE notification_digest_state
    ADD COLUMN IF NOT EXISTS config_signature text NOT NULL DEFAULT '';

-- Existing schedules predate the signature column. Preserve their current
-- subscription semantics so the first v0.1.3 worker pass does not silently
-- discard an already-due digest window.
UPDATE notification_digest_state AS state
SET config_signature = CASE COALESCE(preferences.payload#>>'{notifications,digest,mode}', 'off')
    WHEN 'off' THEN 'off'
    WHEN 'hourly' THEN 'hourly'
    WHEN 'daily' THEN 'daily@' || COALESCE(preferences.payload#>>'{notifications,digest,time}', '08:00')
    ELSE 'invalid'
END
FROM user_preferences AS preferences
WHERE preferences.user_id = state.user_id
  AND state.config_signature = '';
