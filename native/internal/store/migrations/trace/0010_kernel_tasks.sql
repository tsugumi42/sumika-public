-- +goose Up
CREATE TABLE kernel_mcp_calls (
 id TEXT PRIMARY KEY, server_id TEXT NOT NULL, state TEXT NOT NULL,
 binding TEXT NOT NULL, reason TEXT NOT NULL DEFAULT ''
);
CREATE INDEX kernel_mcp_unknown ON kernel_mcp_calls(server_id,state);
CREATE TABLE kernel_tasks (
 id TEXT PRIMARY KEY, parent_run TEXT NOT NULL, tool_id TEXT NOT NULL,
 payload_digest TEXT NOT NULL, owner TEXT NOT NULL, root_id TEXT NOT NULL,
 state TEXT NOT NULL, payload TEXT NOT NULL, result TEXT NOT NULL DEFAULT '',
 UNIQUE(parent_run,tool_id)
);
CREATE TABLE kernel_task_mailbox (
 task_id TEXT PRIMARY KEY REFERENCES kernel_tasks(id), owner TEXT NOT NULL,
 state TEXT NOT NULL, receiving_run TEXT NOT NULL DEFAULT ''
);
CREATE TABLE kernel_web_usage (
 id TEXT PRIMARY KEY, owner TEXT NOT NULL, root_id TEXT NOT NULL,
 state TEXT NOT NULL, credits INTEGER NOT NULL, actual INTEGER
);
CREATE TABLE kernel_web_quota (
 dimension TEXT NOT NULL, window TEXT NOT NULL,
 used INTEGER NOT NULL, PRIMARY KEY(dimension,window)
);
CREATE TABLE kernel_web_charges (
 request_id TEXT NOT NULL REFERENCES kernel_web_usage(id),
 dimension TEXT NOT NULL, window TEXT NOT NULL, amount INTEGER NOT NULL,
 PRIMARY KEY(request_id,dimension,window)
);

-- +goose Down
DROP TABLE kernel_web_charges;
DROP TABLE kernel_web_quota;
DROP TABLE kernel_web_usage;
DROP TABLE kernel_task_mailbox;
DROP TABLE kernel_tasks;
DROP TABLE kernel_mcp_calls;
