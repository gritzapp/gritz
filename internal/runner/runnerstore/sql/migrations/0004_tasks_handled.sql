ALTER TABLE tasks ADD COLUMN handled INTEGER NOT NULL DEFAULT 0;  -- id of the last event the task's follower handled; 0 for none
