# App-state delivery boundary: regression-only draft

This draft adds characterization tests, not a persistence fix. They intentionally
assert an unresolved failure so the baseline stays reproducible. A future fix
must replace the stale-state expectations with assertions of the chosen
durability contract; passing these tests does not establish repair.

## User-visible failure and reproduced scope

A background app-state update can advance the SDK's protocol cursor before wacli
has saved archive, pin or unread state. An assistant can then see stale local
state after restart, while an incremental fetch starts beyond the consumed
update. Request acceptance, an idle App queue and the SDK cursor do not prove
that the App projection is durable. A persistence acknowledgment would describe
local storage, independently of WhatsApp delivery or read receipts.

The test uses synthetic keys, SDK EncodePatch, real authenticated decoding,
ApplyAppStatePatches, the actual collector and temporary SDK/App SQLite stores.
The App handler is registered before consumption. It performs no Connect,
network send, account access or real CLI operation.

| Controlled schedule | State after new-process restart |
| --- | --- |
| Hold the generated callbacks, then SIGKILL the producer | SDK cursor 1 and archived=true; App archive=false, pin=false, unread=true/count=2; recovery debt=false |
| Valid first patch followed by a deliberately corrupt second patch | Decoder reports error; cursor 1 persists; no callbacks reach the collector; App remains stale and debt=false |
| Deliver callbacks and wait for App persistence | Cursor 1; App archive=true, pin=true, unread=false/count=0 |

The process driver verifies SIGKILL rather than treating DB close/reopen as a
crash. The final row is a positive fixture control and uses a graceful producer
exit. These are deterministic injected schedules, not measurements of natural
incidence. The partial-patch case does not establish that real servers produce
corrupt patches. No message loss, duplicates, outgoing-send deduplication,
network recovery or universal explanation of history failures is established.
The fake-interface send counters are per worker and are not a cumulative SDK
transport audit.

## Pinned baseline and source boundary

Base: `4d6540e2ee512b957bd90b67bfdeb11eac56d5d4` (separate from this draft's
changes). SDK: `go.mau.fi/whatsmeow v0.0.0-20261017175727-b2b1e71d45e2`,
commit `b2b1e71d45e2ff171153b025b5a42f475bb30ef8`. This draft changes no dependency.

At that SDK pin:

- `appstate/decode.go`, `Processor.storeMACs`: cursor/MAC storage occurs before
  `Client.collectEventsToDispatch` in `appstate.go`.
- `Client.dispatchAppState` creates typed events without a durable delivery
  identity. `Mutation.PatchVersion` alone cannot identify equal-version reset
  payloads or provide a store generation/local delivery sequence.
- `Client.fetchAppState` deletes version state before a full fetch;
  `handleAppStateRecovery` preserves the actual peer freshness gate;
  `sendAppState` applies conflicts and dispatches fetched events asynchronously.
- SQLStore's `PutAppStateMutationMACs` uses native transaction machinery, but
  there is no encompassing verified-payload/cursor/reset transaction today.
  The existing dbutil transaction context can support such a targeted extension.

In wacli, `internal/app/chat_state.go` has both explicit local-write completion
and fetched/recovered event persistence. `internal/app/sync_events.go` admits
live callbacks and recovery debt. The handler also retains Star, DeleteForMe,
Contact and generic AppState processing. Removing the handler to create a
single owner silently drops those features. The durable recovery-intent table
holds collection/generation debt, not replayable payload. ContactStoreJournal
is in memory, not a durable handoff.

## Why this draft stops at regressions

The prior synchronous pre-commit prototype was disproved by stricter failure
checks: App changes could remain committed when the SDK commit failed, including
partial App application. Cursor zero was not sufficient evidence of a safe
refusal. A research outbox prototype also used protocol version as identity;
replacement/checkpoint probes exposed collisions, and disabling the complete
handler dropped unrelated features. Those results reject those prototypes as
production fixes.

Native SDK transaction support is feasible. An opt-in delivery-provenance API
is also feasible in principle; its absence is not proof that the SDK makes a fix
impossible or that a rewrite is necessary. However, the required end-to-end
ownership/acknowledgment contract has not been established and validated in a
bounded source carry. In particular, a staged payload alone does not fence old
callbacks or order the existing explicit local-write completion against newer
SDK state. This is an implementation/proof blocker, not a permissions blocker.
The rejected observer and temporary dependency replacement are not in this draft.

## Candidate bounded-handoff contract still requiring proof

1. Allow one outstanding delivery per store, with no background queue. In one
   native session transaction, stage a bounded verified payload and its exact
   cursor/hash/MAC/reset transition. Reject staging failure without consumption.
2. Identify deliveries by persistent store generation plus local sequence;
   record replacement kind and payload digest. Preserve peer freshness gates.
   Drain the preceding delivery before another consume, reset or local mutation.
3. Atomically commit all covered App projections and the matching checkpoint in
   wacli.db. Acknowledge only that exact delivery after checkpoint success.
   Retain pending payload and report pending projection on any failure.
4. Carry selective provenance to callback admission and accepted local writes.
   Fence only covered projections; retain Star, DeleteForMe, Contact and generic
   handlers and debt for uncovered work. Define local accepted-write ordering;
   neither observed cursor version nor asynchronous callback arrival proves it.
5. Keep SDK protocol/transaction APIs separate from wacli unread, canonical-JID,
   workflow reporting and SQL policy. Any dependency carry needs exact upstream
   provenance, a minimal patch series, independent nested-module tests and a
   removal criterion. No wholesale ownership rewrite is justified by this test.

Acceptance must exercise the same encrypted producer fixture before and after:
crash within each SDK write and App projection, checkpoint-before-ack restart,
failed App/log/cursor writes with explicit errors and unchanged/partial-state
assertions, app-ahead retry, payload bounds, replacement and reset identities,
newer concurrent updates, idempotent replay and cumulative transport auditing.
Also required: native SQLite/PostgreSQL transaction tests, mixed-feature callback
preservation, actual send/conflict paths, cancellation/deadlocks, exact unread
ranges and PN/LID mappings, real CLI status against disposable stores, and the
repository plain/FTS/race/full gate. Live validation is a separate later task.

## Run the focused characterization

```sh
go test ./internal/app -run '^TestAppStateDeliveryGap' -count=10 -v
go test -race ./internal/app -run '^TestAppStateDeliveryGap' -count=3 -v
go test -tags sqlite_fts5 ./internal/app -run '^TestAppStateDeliveryGap' -count=3 -v
```

The process-kill assertion is skipped on Windows. This test is a prerequisite
for reviewing a fix, not an acceptance gate that a repaired implementation
should continue satisfying unchanged.
