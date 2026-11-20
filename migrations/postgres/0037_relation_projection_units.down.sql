ALTER TABLE projection_runs DROP COLUMN IF EXISTS unit_failed;
ALTER TABLE projection_runs DROP COLUMN IF EXISTS unit_succeeded;
ALTER TABLE projection_runs DROP COLUMN IF EXISTS unit_total;
DROP TABLE IF EXISTS projection_units;