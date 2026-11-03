-- v18 -> v19: Retain one retired delivery independently of credential deletion
ALTER TABLE whatsmeow_app_state_delivery ADD COLUMN prior_epoch bytea;
CREATE TABLE whatsmeow_app_state_retired_delivery (
 slot INTEGER PRIMARY KEY CHECK(slot=1),
 jid TEXT NOT NULL,
 generation TEXT NOT NULL,
 sequence BIGINT NOT NULL CHECK(sequence>=0),
 name TEXT,kind TEXT,before_version BIGINT,after_version BIGINT,
 before_hash bytea,after_hash bytea,digest bytea,payload bytea,
 epoch_kind TEXT NOT NULL,
 predecessor_sequence BIGINT,predecessor_digest bytea,prior_epoch bytea
);
