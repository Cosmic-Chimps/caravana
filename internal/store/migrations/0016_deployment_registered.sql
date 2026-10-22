-- Feature 021: when Nomad accepted a deployment's job.
--
-- The stages strip on the Deployments tab says how long a deployment waited
-- (created to registered) and how long its rollout took (registered to the
-- end). NULL for a deployment that never registered, and for those recorded
-- before; the page then says nothing of the two spans.
ALTER TABLE deployments ADD COLUMN registered_at TEXT;
