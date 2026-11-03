package app

import (
	"bytes"
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
	if d.ID != d.Boundary.ID {
		return fmt.Errorf("app state delivery identity/provenance mismatch")
	}
	if err := a.validateAppStateDeliveryBoundary(ctx, &d.Boundary); err != nil {
		return err
	}
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
	if d.Boundary.Retired {
		for _, m := range mutations {
			if m.Operation != waServerSync.SyncdMutation_SET || m.Action == nil || len(m.Index) == 0 {
				return fmt.Errorf("retired app state contains uncovered work; evidence retained, independent recovery required")
			}
			switch m.Index[0] {
			case appstate.IndexArchive, appstate.IndexPin, appstate.IndexMute, appstate.IndexMarkChatAsRead, appstate.IndexSettingUnarchiveChats:
			default:
				return fmt.Errorf("retired app state contains uncovered work; evidence retained, independent recovery required")
			}
		}
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
	err = a.db.ApplyAppStateDelivery(ctx, checkpointForBoundary(&d.Boundary), actions, debt)
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

func checkpointForBoundary(b *sdkstore.AppStateDeliveryBoundary) appstore.DeliveryCheckpoint {
	cp := deliveryCheckpoint(b.ID)
	cp.Device = b.Device
	if b.PriorEpoch != nil {
		prior := deliveryCheckpoint(b.PriorEpoch.ID)
		prior.Device = b.PriorEpoch.Device
		cp.PriorEpoch = &prior
	}
	if b.Predecessor != nil {
		prev := deliveryCheckpoint(*b.Predecessor)
		prev.Device = b.Device
		cp.Predecessor = &prev
	}
	return cp
}

// This validator runs under the SDK producer gate before startup success,
// covered SDK cache writes, native consumption or acknowledgment.
func (a *App) validateAppStateDeliveryBoundary(ctx context.Context, b *sdkstore.AppStateDeliveryBoundary) error {
	checkpoints, err := a.db.AppStateDeliveryCheckpoints(ctx)
	if err != nil {
		return err
	}
	refuse := func(reason string) error {
		return fmt.Errorf("app state store pairing refused (%s); pending evidence preserved: use matching stores or independently verified recovery; do not reset checkpoints or downgrade", reason)
	}
	if b == nil {
		if len(checkpoints) == 0 {
			return nil
		}
		return refuse("session/device missing for existing App checkpoints")
	}
	if b.Retired {
		debt, err := a.db.AppStateRecoveryCollections()
		if err != nil {
			return err
		}
		if len(debt) != 0 {
			return refuse("retired epoch has unresolved recovery debt; independent recovery required")
		}
	}
	if b.Device == "" || b.ID.Generation == "" {
		return refuse("missing device/epoch")
	}
	priorMatches := func() bool {
		if b.PriorEpoch == nil || len(checkpoints) != 1 {
			return false
		}
		cp := checkpoints[0]
		return cp.Device == b.PriorEpoch.Device && cp.Generation == b.PriorEpoch.ID.Generation && cp.Sequence == b.PriorEpoch.ID.Sequence && bytes.Equal(cp.Digest, b.PriorEpoch.ID.Digest[:])
	}
	if b.ID.Sequence == 0 {
		if b.Epoch == "fresh" && (len(checkpoints) == 0 && b.PriorEpoch == nil || priorMatches()) {
			return nil
		}
		return refuse("fresh epoch conflicts with App checkpoint history")
	}
	var cp *appstore.DeliveryCheckpoint
	for i := range checkpoints {
		if checkpoints[i].Generation == b.ID.Generation {
			cp = &checkpoints[i]
			break
		}
	}
	if cp == nil {
		if b.Pending && b.ID.Sequence == 1 && b.Predecessor == nil && (len(checkpoints) == 0 && b.PriorEpoch == nil || priorMatches()) {
			return nil
		}
		return refuse("missing matching checkpoint or unvalidated epoch")
	}
	if cp.Device != "" && cp.Device != b.Device {
		return refuse("device binding mismatch")
	}
	if cp.Device == "" && b.Epoch != "legacy17" {
		return refuse("unbound checkpoint in successor epoch")
	}
	if cp.Sequence == b.ID.Sequence && bytes.Equal(cp.Digest, b.ID.Digest[:]) {
		// Native ACK/current payload is independent legacy evidence. Bind it with
		// the existing atomic checkpoint transaction, without replaying projections.
		if cp.Device == "" {
			return a.db.ApplyAppStateDelivery(ctx, checkpointForBoundary(b), nil, nil)
		}
		return nil
	}
	if b.Pending && b.Predecessor != nil && b.Predecessor.Generation == b.ID.Generation && b.Predecessor.Sequence+1 == b.ID.Sequence && cp.Sequence == b.Predecessor.Sequence && bytes.Equal(cp.Digest, b.Predecessor.Digest[:]) {
		return nil
	}
	return refuse("checkpoint identity/digest differs; legacy predecessor may be unavailable")
}
