-- +goose Up
CREATE TABLE root_budget (
 root_id TEXT PRIMARY KEY,
 state_json BLOB NOT NULL
);
CREATE TABLE budget_reservations (
 id TEXT PRIMARY KEY,
 root_id TEXT NOT NULL REFERENCES root_budget(root_id)
);
CREATE INDEX budget_reservations_root ON budget_reservations(root_id);
ALTER TABLE model_runs ADD COLUMN root_id TEXT;
ALTER TABLE model_runs ADD COLUMN run_id TEXT;
ALTER TABLE model_runs ADD COLUMN step_id TEXT;
ALTER TABLE model_runs ADD COLUMN attempt_id TEXT;
ALTER TABLE model_runs ADD COLUMN purpose TEXT;
ALTER TABLE model_runs ADD COLUMN usage_seen INTEGER;
ALTER TABLE model_runs ADD COLUMN incomplete INTEGER;
ALTER TABLE model_runs ADD COLUMN cost_micros INTEGER;
ALTER TABLE model_runs ADD COLUMN reset_reason TEXT;
CREATE UNIQUE INDEX model_runs_attempt ON model_runs(attempt_id) WHERE attempt_id IS NOT NULL;

-- +goose Down
DROP INDEX model_runs_attempt;
ALTER TABLE model_runs DROP COLUMN reset_reason;
ALTER TABLE model_runs DROP COLUMN cost_micros;
ALTER TABLE model_runs DROP COLUMN incomplete;
ALTER TABLE model_runs DROP COLUMN usage_seen;
ALTER TABLE model_runs DROP COLUMN purpose;
ALTER TABLE model_runs DROP COLUMN attempt_id;
ALTER TABLE model_runs DROP COLUMN step_id;
ALTER TABLE model_runs DROP COLUMN run_id;
ALTER TABLE model_runs DROP COLUMN root_id;
DROP TABLE budget_reservations;
DROP TABLE root_budget;
