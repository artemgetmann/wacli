-- v16 -> v17: One outstanding verified app-state delivery per device store
CREATE TABLE whatsmeow_app_state_delivery (
 jid TEXT PRIMARY KEY REFERENCES whatsmeow_device(jid) ON DELETE CASCADE,
 generation TEXT NOT NULL,
 sequence BIGINT NOT NULL CHECK (sequence >= 0),
 name TEXT,
 kind TEXT,
 before_version BIGINT,
 after_version BIGINT,
 before_hash bytea,
 after_hash bytea,
 digest bytea,
 payload bytea
);
