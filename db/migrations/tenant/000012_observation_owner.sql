-- Observation ownership anchor.
--
-- A memory's owner was only derivable from a bound session
-- (observed_events.session_id -> sessions.user_id), which made session-less
-- user-global writes silently unownable. Record the trusted owner on the
-- observation itself. The column is nullable: historical session-less events
-- have no owner to backfill and continue to resolve through the session
-- fallback at write time.
ALTER TABLE observed_events ADD COLUMN IF NOT EXISTS user_id uuid;

-- Backfill the owner for events that were already bound to a session.
UPDATE observed_events oe
SET user_id = s.user_id
FROM sessions s
WHERE oe.session_id = s.id
  AND oe.user_id IS NULL;
