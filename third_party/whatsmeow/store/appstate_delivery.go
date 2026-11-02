// Copyright (c) 2026 The wacli contributors
// SPDX-License-Identifier: MPL-2.0
package store

import (
	"context"
	"errors"
)

// MaxAppStateDeliveryBytes bounds the one outstanding payload before consumption.
const MaxAppStateDeliveryBytes = 8 << 20

var ErrAppStateDeliveryPending = errors.New("app state projection pending")
var ErrAppStateDeliveryIdentity = errors.New("app state delivery identity mismatch")

// AppStateDeliveryID identifies local consumption, independently of protocol
// versions, which may repeat or move backwards during explicit replacement.
type AppStateDeliveryID struct {
	Generation string
	Sequence   uint64
	Digest     [32]byte
}

// AppStateTransition describes an already verified protocol transition. Payload
// is a versioned consumer envelope. Kind is delta, snapshot, recovery or reset.
// Before is checked inside the same native transaction that stages the payload.
type AppStateTransition struct {
	Name, Kind                  string
	BeforeVersion, AfterVersion uint64
	BeforeHash, AfterHash       [128]byte
	Payload                     []byte
	RemovedMACs                 [][]byte
	AddedMACs                   []AppStateMutationMAC
}

// AppStateDeliveryBoundary is durable native provenance, including after ACK.
// Fresh epochs describe a tracking boundary, not prior projection completeness.
type AppStateEpochIdentity struct {
	Device string
	ID     AppStateDeliveryID
}

// AppStateDeliveryLifecycle exposes a bounded retired slot without credentials.
// Consumers must finish or refuse it before admitting a replacement device.
type AppStateDeliveryLifecycle interface {
	RetiredAppStateDeliveryStore(context.Context) (AppStateDeliveryStore, error)
}

type AppStateDeliveryBoundary struct {
	Retired     bool
	PriorEpoch  *AppStateEpochIdentity
	Device      string
	Epoch       string
	ID          AppStateDeliveryID
	Predecessor *AppStateDeliveryID
	Name        string
	Version     uint64
	Hash        [128]byte
	Pending     bool
}

type AppStateDelivery struct {
	Boundary   AppStateDeliveryBoundary
	ID         AppStateDeliveryID
	Transition AppStateTransition
}

// AppStateDeliveryStore is opt-in. Implementations must atomically persist the
// payload with cursor/hash/MAC/reset, allow only one outstanding delivery per
// device store, and never acknowledge a different generation/sequence/digest.
// Acknowledgment means consumer checkpoint durability, not a WhatsApp receipt.
type AppStateDeliveryStore interface {
	AppStateDeliveryBoundary(context.Context) (*AppStateDeliveryBoundary, error)
	StageAppStateDelivery(context.Context, AppStateTransition) (*AppStateDelivery, error)
	PendingAppStateDelivery(context.Context) (*AppStateDelivery, error)
	AckAppStateDelivery(context.Context, AppStateDeliveryID) error
}
