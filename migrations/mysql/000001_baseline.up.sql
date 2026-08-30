CREATE TABLE search_event_inbox (
    consumer VARCHAR(255) NOT NULL,
    event_id VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL,
    attempts INT NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL,
    completed_at TIMESTAMP(6) NULL,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMP(6) NOT NULL,
    updated_at TIMESTAMP(6) NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    PRIMARY KEY (consumer, event_id),
    INDEX search_event_inbox_status_updated_idx (status, updated_at)
) ENGINE=InnoDB;
