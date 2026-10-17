-- Backfill the Session and Consolidate capabilities onto agents that predate
-- them, so existing credentials keep passing the new capability gates without
-- credential rotation. The @> guards make both statements idempotent: re-running
-- the migration never appends a duplicate capability.

UPDATE public.agents
   SET capabilities = capabilities || ARRAY['session']
 WHERE capabilities @> ARRAY['observe']
   AND NOT capabilities @> ARRAY['session'];

UPDATE public.agents
   SET capabilities = capabilities || ARRAY['consolidate']
 WHERE capabilities @> ARRAY['project']
   AND NOT capabilities @> ARRAY['consolidate'];
