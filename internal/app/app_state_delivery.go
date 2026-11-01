package app

import (
	"context"
	"fmt"
	"time"

	appstore "github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	sdkstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func deliveryCheckpoint(id sdkstore.AppStateDeliveryID) appstore.DeliveryCheckpoint {
	return appstore.DeliveryCheckpoint{Generation: id.Generation, Sequence: id.Sequence, Digest: id.Digest[:]}
}
func deliveryIDForEvent(evt any) *sdkstore.AppStateDeliveryID {
	switch e := evt.(type) {
	case *events.Archive:
		if e != nil {
			return e.DeliveryID
		}
	case *events.Pin:
		if e != nil {
			return e.DeliveryID
		}
	case *events.Mute:
		if e != nil {
			return e.DeliveryID
		}
	case *events.MarkChatAsRead:
		if e != nil {
			return e.DeliveryID
		}
	case *events.UnarchiveChatsSetting:
		if e != nil {
			return e.DeliveryID
		}
	}
	return nil
}

// consumeAppStateDelivery never waits on callback admission or reserved local
// tasks. The SDK owns serialization and acks only after this checkpoint commits.
func (a *App) consumeAppStateDelivery(ctx context.Context, d *sdkstore.AppStateDelivery) error {
	done, err := a.db.HasExactAppStateDeliveryCheckpoint(ctx, deliveryCheckpoint(d.ID))
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	mutations, err := appstate.DecodeDeliveryMutations(d.Transition.Payload)
	if err != nil {
		return err
	}
	var actions []appstore.ChatStateProjection
	var observations []any
	var debt []string
	for _, m := range mutations {
		if m.Operation != waServerSync.SyncdMutation_SET || m.Action == nil || len(m.Index) == 0 {
			debt = append(debt, d.Transition.Name)
			continue
		}
		switch m.Index[0] {
		case appstate.IndexArchive, appstate.IndexPin, appstate.IndexMute, appstate.IndexMarkChatAsRead, appstate.IndexSettingUnarchiveChats:
		default:
			debt = append(debt, d.Transition.Name)
			continue
		}
		var jid types.JID
		if len(m.Index) > 1 {
			jid, err = types.ParseJID(m.Index[1])
			if err != nil {
				return fmt.Errorf("invalid app state chat identity: %w", err)
			}
			jid = a.canonicalStoreJID(ctx, jid)
		}
		at := time.UnixMilli(m.Action.GetTimestamp())
		projection := appstore.ChatStateProjection{JID: jid.String(), Timestamp: at}
		switch m.Index[0] {
		case appstate.IndexArchive:
			if m.Action.ArchiveChatAction == nil {
				return fmt.Errorf("missing archive action")
			}
			projection.Kind = "archive"
			projection.Enabled = m.Action.GetArchiveChatAction().GetArchived()
			observations = append(observations, &events.Archive{JID: jid, Timestamp: at, Action: m.Action.GetArchiveChatAction()})
		case appstate.IndexPin:
			if m.Action.PinAction == nil {
				return fmt.Errorf("missing pin action")
			}
			projection.Kind = "pin"
			projection.Enabled = m.Action.GetPinAction().GetPinned()
		case appstate.IndexMute:
			if m.Action.MuteAction == nil {
				return fmt.Errorf("missing mute action")
			}
			projection.Kind = "mute"
			projection.MutedUntil = mutedUntilFromAction(m.Action.GetMuteAction())
		case appstate.IndexMarkChatAsRead:
			if m.Action.MarkChatAsReadAction == nil {
				return fmt.Errorf("missing read action")
			}
			projection.Kind = "read"
			projection.Enabled = m.Action.GetMarkChatAsReadAction().GetRead()
			projection.ReadThrough, projection.ReadIDs = markChatAsReadPosition(&events.MarkChatAsRead{Timestamp: at, Action: m.Action.GetMarkChatAsReadAction()})
		case appstate.IndexSettingUnarchiveChats:
			if m.Action.GetUnarchiveChatsSetting() == nil || m.Action.GetUnarchiveChatsSetting().UnarchiveChats == nil {
				return fmt.Errorf("missing archive setting")
			}
			projection.Kind = "archive_setting"
			projection.Enabled = m.Action.GetUnarchiveChatsSetting().GetUnarchiveChats()
			observations = append(observations, &events.UnarchiveChatsSetting{Timestamp: at, Action: m.Action.GetUnarchiveChatsSetting()})
		default:
			debt = append(debt, d.Transition.Name)
			continue
		}
		if projection.Kind != "archive_setting" && jid.IsEmpty() {
			return fmt.Errorf("missing covered app state chat identity")
		}
		actions = append(actions, projection)
	}
	for _, evt := range observations {
		a.observeArchiveEvent(ctx, evt, false)
	}
	err = a.db.ApplyAppStateDelivery(ctx, deliveryCheckpoint(d.ID), actions, debt)
	if err == nil {
		for _, evt := range observations {
			a.observeArchiveEvent(ctx, evt, true)
		}
	}
	return err
}

// Completion comes only from the exact SDK-owned producer boundary. Seeing
// a generic event is not proof that its typed callbacks belong to this response.
func (a *App) settleFetchedAppStateDeliveryRecovery(ctx context.Context, collection appstate.WAPatchName, evts []any) error {
	seen := make(map[sdkstore.AppStateDeliveryID]bool)
	var ids []appstore.DeliveryCheckpoint
	for _, evt := range evts {
		complete, ok := evt.(*events.AppStateSyncComplete)
		if !ok || complete == nil || complete.Name != collection {
			continue
		}
		for _, id := range complete.CompletedDeliveryIDs {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, deliveryCheckpoint(id))
			}
		}
	}
	return a.db.ClearFetchedDeliveryRecovery(ctx, string(collection), ids)
}

// eventDeliveryProvenance filters snapshot membership only. It does not fence
// uncovered live persistence or replace the existing unowned handlers.
func eventDeliveryProvenance(evt any) *sdkstore.AppStateDeliveryID {
	switch e := evt.(type) {
	case *events.AppState:
		if e != nil {
			return e.DeliveryID
		}
	case *events.Star:
		if e != nil {
			return e.DeliveryID
		}
	case *events.DeleteForMe:
		if e != nil {
			return e.DeliveryID
		}
	case *events.Contact:
		if e != nil {
			return e.DeliveryID
		}
	}
	return deliveryIDForEvent(evt)
}
