package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/linkpreview"
	"github.com/openclaw/wacli/internal/store"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

type textReplyReadState = app.HandledReplyReadState

const handledTextKind = "text_handled_reply"

func textDelegateKind(through string) string {
	if through != "" {
		return handledTextKind
	}
	return "text"
}

// sendTextWithHistory is shared by direct and socket-delegated sends. Persist
// only after acceptance, using the actual native context from the final attempt.
// A local save failure remains a warning and must never retry an accepted send.
func sendTextWithHistory(ctx context.Context, a sendTextApp, reconnect func(context.Context) error, to types.JID, text, replyTo, replyToSender string, preview *linkpreview.Preview, mentions []string, ephemeral textEphemeralOptions, options textSendOptions) (types.MessageID, error, error) {
	if options.localRead != nil {
		*options.localRead = textReplyReadState{Status: "not_attempted"}
	}
	chat := canonicalOutboundChat(ctx, a.WA(), to)
	state := textReplyReadState{Status: "preserved_unknown_scope"}
	var snapshot *store.HandledUnreadSnapshot
	var readErr error
	if options.preserveUnread {
		state.Status = "preserved"
	} else if options.handledThrough != "" {
		// A quote or latest-at-send lookup does not establish what was answered.
		// Validate caller-supplied scope, then freeze known pending IDs before
		// sending. Unknown membership defers acknowledgement, never the reply.
		if _, err := a.DB().ReadBoundary(chat.String(), options.handledThrough); err != nil {
			return "", nil, err
		}
		state.ThroughMessageID = options.handledThrough
		snapshot, readErr = a.DB().SnapshotHandledUnread(chat.String(), options.handledThrough)
		state.Status = "deferred"
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
	if storeErr == nil && snapshot != nil && readErr == nil {
		state, readErr = app.CompleteHandledReply(ctx, a.DB(), a.WA(), chat, snapshot)
	}
	if readErr != nil {
		state.Warning = readErr.Error()
	}
	if storeErr != nil {
		state.Status = "not_attempted_save_failed"
	}
	if options.localRead != nil {
		*options.localRead = state
	}
	// The accepted send identity survives acknowledgement or save warnings.
	// Callers must never resend to repair these independent outcomes.
	if readErr != nil {
		readErr = fmt.Errorf("reply accepted; unread acknowledgement deferred or uncertain: %w", readErr)
	}
	return id, errors.Join(storeErr, readErr), nil
}
