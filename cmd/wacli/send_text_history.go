package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openclaw/wacli/internal/linkpreview"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

type textReplyReadState struct {
	Status           string `json:"status"`
	ThroughMessageID string `json:"through_message_id,omitempty"`
	DeviceVerified   bool   `json:"device_verified"`
}

// sendTextWithHistory is shared by direct and socket-delegated sends. Persist
// only after acceptance, using the actual native context from the final attempt.
// A local save failure remains a warning and must never retry an accepted send.
func sendTextWithHistory(ctx context.Context, a sendTextApp, reconnect func(context.Context) error, to types.JID, text, replyTo, replyToSender string, preview *linkpreview.Preview, mentions []string, ephemeral textEphemeralOptions, options textSendOptions) (types.MessageID, error, error) {
	if options.localRead != nil {
		*options.localRead = textReplyReadState{Status: "not_attempted"}
	}
	chat := canonicalOutboundChat(ctx, a.WA(), to).String()
	// Capture before dispatch. Completion time would include messages that
	// arrived while the reply was being sent and were never answered.
	var boundaryID string
	var boundaryTime time.Time
	var boundaryErr error
	if !options.preserveUnread {
		boundary, err := a.DB().LatestReplyBoundary(chat)
		if err == nil {
			boundaryID, boundaryTime = boundary.MsgID, boundary.Timestamp
		} else if !errors.Is(err, sql.ErrNoRows) {
			boundaryErr = fmt.Errorf("read-state snapshot: %w", err)
		}
	}
	var replyContext *waProto.ContextInfo
	options.replyContext = &replyContext
	id, err := runSendOperation(ctx, reconnect, func(ctx context.Context) (types.MessageID, error) {
		replyContext = nil
		return sendTextMessage(ctx, a, to, text, replyTo, replyToSender, preview, mentions, ephemeral, options)
	})
	if err != nil {
		return "", nil, err
	}
	storeErr := persistOutboundTextWith(ctx, a.DB(), a.WA(), to, string(id), text, time.Now().UTC(), replyContext)
	if options.localRead != nil {
		if options.preserveUnread {
			options.localRead.Status = "preserved"
		} else if boundaryID == "" && boundaryErr == nil {
			options.localRead.Status = "no_inbound"
		} else {
			options.localRead.Status = "failed"
		}
	}
	if storeErr == nil && boundaryErr == nil && boundaryID != "" {
		if err := a.DB().ClearChatUnreadThroughPreservingMarker(chat, boundaryTime, []string{boundaryID}); err != nil {
			storeErr = fmt.Errorf("reply accepted and saved, local unread update failed: %w", err)
		} else if options.localRead != nil {
			options.localRead.Status = "cleared_through"
			options.localRead.ThroughMessageID = boundaryID
		}
	}
	return id, errors.Join(storeErr, boundaryErr), nil
}
