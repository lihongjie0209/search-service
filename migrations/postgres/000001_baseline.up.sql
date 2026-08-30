CREATE TABLE search_event_inbox (
    consumer TEXT NOT NULL,
    event_id TEXT NOT NULL,
    status TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    completed_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    PRIMARY KEY (consumer, event_id)
);
CREATE INDEX search_event_inbox_status_updated_idx ON search_event_inbox (status, updated_at);
