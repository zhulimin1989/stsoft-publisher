-- Revert to the (buggy) CACHE 100 setting. Only run if also reverting
-- the surrounding HA code so the monotonicity violation doesn't surface
-- as silent write loss.

ALTER SEQUENCE publisher_revision_seq CACHE 100;
