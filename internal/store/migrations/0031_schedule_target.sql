-- Feature 040: a schedule may run its command in the service's running copy
-- instead of a fresh one (target 'copy'), in one copy or in every healthy copy.

ALTER TABLE schedules ADD COLUMN target TEXT NOT NULL DEFAULT 'fresh' CHECK (target IN ('fresh', 'copy'));
ALTER TABLE schedules ADD COLUMN all_copies INTEGER NOT NULL DEFAULT 0;

-- A run keeps the target it ran with: the schedule can be edited while it goes.
-- copies is a JSON array of the copy (allocation) IDs a copy run ran in, NULL
-- for a fresh run.
ALTER TABLE schedule_runs ADD COLUMN target TEXT NOT NULL DEFAULT 'fresh' CHECK (target IN ('fresh', 'copy'));
ALTER TABLE schedule_runs ADD COLUMN copies TEXT;
