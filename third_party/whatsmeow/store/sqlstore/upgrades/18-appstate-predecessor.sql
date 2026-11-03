-- v17 -> v18: Fence older writers and retain the predecessor of the one pending delivery
ALTER TABLE whatsmeow_app_state_delivery ADD COLUMN epoch_kind TEXT NOT NULL DEFAULT 'legacy17';
ALTER TABLE whatsmeow_app_state_delivery ADD COLUMN predecessor_sequence BIGINT;
ALTER TABLE whatsmeow_app_state_delivery ADD COLUMN predecessor_digest bytea;
