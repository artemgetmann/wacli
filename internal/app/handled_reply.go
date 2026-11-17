package app

import (
	"context"
	"fmt"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

// HandledReplyReadState separates socket dispatch, local settlement and device
// observation. A successful write is not a server acknowledgement or badge proof.
type HandledReplyReadState struct {
	Status           string `json:"status"`
	ThroughMessageID string `json:"through_message_id,omitempty"`
	DeviceVerified   bool   `json:"device_verified"`
	ReceiptType      string `json:"receipt_type,omitempty"`
	Receipts         int    `json:"receipts,omitempty"`
	Warning          string `json:"warning,omitempty"`
}

// CompleteHandledReply is called only after an accepted send has been saved.
// The snapshot was captured before dispatch from explicitly handled input. It
// cannot be reconstructed from the latest message or selected after local clear.
func CompleteHandledReply(ctx context.Context, db *store.DB, client WAClient, chat types.JID, snapshot *store.HandledUnreadSnapshot) (state HandledReplyReadState, err error) {
	state.Status = "deferred"
	state.ThroughMessageID = snapshot.ThroughID
	if err = ctx.Err(); err != nil {
		return state, err
	}
	// Durable claims both forbid a second attempt and protect against receipt
	// echoes before completion or after reopening. Never erase a claim on error.
	if err = db.ClaimHandledUnread(snapshot); err != nil {
		return state, err
	}
	batches, err := readReceiptBatches(snapshot.Messages, chat.Server == types.GroupServer)
	if err != nil {
		return state, err
	}
	var addressing types.AddressingMode
	if chat.Server == types.GroupServer {
		info, lookupErr := client.GetGroupInfo(ctx, chat)
		if lookupErr != nil || info == nil {
			return state, fmt.Errorf("private acknowledgement addressing unavailable: %v", lookupErr)
		}
		addressing = info.AddressingMode
	}
	state.ReceiptType = string(types.ReceiptTypeReadSelf)
	for _, batch := range batches {
		for start := 0; start < len(batch.ids); start += maxReadReceiptMessages {
			if err = ctx.Err(); err != nil {
				state.Status = "uncertain"
				return state, err
			}
			ids := batch.ids[start:min(start+maxReadReceiptMessages, len(batch.ids))]
			// Explicit private transport only. Timeout/partial failure never falls
			// back to ordinary receipts, app-state writes or a repeated request.
			ackCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = client.MarkReadSelf(ackCtx, ids, time.Now().UTC(), chat, batch.sender, addressing)
			cancel()
			if err != nil {
				state.Status = "uncertain"
				return state, fmt.Errorf("private acknowledgement outcome uncertain; no retry: %w", err)
			}
			state.Receipts += len(ids)
		}
	}
	state.Status = "dispatched_unsettled"
	settled, err := db.SettleHandledUnread(snapshot)
	if err != nil {
		return state, err
	}
	if !settled {
		return state, fmt.Errorf("private acknowledgement dispatched; ambiguous local unread retained")
	}
	state.Status = "dispatched"
	return state, nil
}
