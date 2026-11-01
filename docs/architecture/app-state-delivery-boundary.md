# App-state durable handoff candidate

The SDK can consume an update before asynchronous callbacks save the App view.
This candidate retains verified updates until the covered App projections and
an exact checkpoint commit. It is isolated implementation work pending final
review, not a claim of live-device convergence or deployment.

## Covered behavior

Archive, pin, mute, mark-read/unread and the auto-unarchive preference use the
handoff. Exact unread ranges keep the existing same-second, incomplete-history
and newer-marker rules. Existing Star, DeleteForMe, Contact and generic handlers
remain active; these actions retain bounded conservative collection recovery
debt until a complete replay persists them. That debt is containment, not local
payload replay for those uncovered projections.

| Boundary | What may be claimed |
| --- | --- |
| Successful IQ response | WhatsApp accepted the signed request; local storage may still fail |
| Native SDK transaction committed | Cursor/hash/MAC/reset and one bounded replay payload are saved together |
| App transaction committed | Covered projections, checkpoint and uncovered-work debt are saved together |
| Exact payload acknowledged | The matching covered App checkpoint committed; later callbacks cannot overwrite it |
| Device delivery/read receipts | Separate evidence; this handoff proves neither |

If IQ acceptance is followed by staging failure, no replay payload may exist.
The command reports accepted-but-incomplete local persistence, keeps its existing
recovery intent, and requires refetch/recovery. It must not resend the accepted
request merely because a local drain has nothing to replay.

## Ownership and storage

`session.db` owns one outstanding delivery per SDK device store. Its identity is
persistent store generation plus local sequence and payload digest, independent
of protocol version. Payloads above 8 MiB fail without consumption; there is no silent truncation or
unverified fallback. The native transaction checks the expected cursor/hash,
updates cursor and MACs, applies replacement/reset, and stages the verified
payload. It rejects oversized payloads, a second outstanding delivery, stale
bases and nested transaction contexts that would return before commit.

The consumer first repairs covered SDK chat-setting caches. It then commits
covered App projections and checkpoint in one wacli.db transaction. An exact
checkpoint replay skips App writes, preserving newer arrivals. Acknowledgment
matches generation, sequence and digest; an old acknowledgment cannot remove a
newer payload. Failure retains pending work and blocks the next consume, reset
or local state mutation. Producer waits are cancelable.

SQLite native staging/acknowledgment requires FULL or EXTRA synchronization.
The CLI configures the session connection accordingly. The App checkpoint uses
FULL synchronization on its dedicated connection and restores its prior setting
before returning it to the normal pool. Process-crash tests do not simulate
power loss, storage hardware failure or dishonest fsync implementations.

Typed covered callbacks carry selective provenance. Their legacy App/SDK cache
writes and stale archive observation hints are suppressed; unrelated handlers
continue normally. Covered local command completion also avoids a second,
obsolete explicit write after a newer verified update has committed.

Uncovered debt is correlated to exact delivery IDs and coalesced to the newest
representative per collection. An older replay cannot clear that newer marker.
Complete fetched, timely-primary and late-primary replays settle only their own
collection's delivery IDs after event persistence and contact-journal drain,
then use the existing generation-clear rule. No single callback clears a batch.

## Recovery, reset and compatibility

A requested full resync validates its replacement before resetting saved SDK
state. A corrupt snapshot leaves the old cursor intact. Explicit from-zero
patch lists use the same verified replacement boundary. Peer recovery retains
the upstream current-version freshness check; local delivery IDs do not bypass
it. Equal/lower versions are meaningful only for an explicit replacement.

App migration 33→34 adds checkpoint/debt-correlation tables without changing
existing chat state or recovery intents. SDK migration 16→17 preserves existing
cursor state and fences older SDK schema compatibility. Existing data already
missed before this upgrade is not reconstructed from cursors; ordinary external
recovery remains necessary for that historical gap.

A missing or mismatched App checkpoint with a retained SDK sequence fails
closed. Restore matching paired-store data; the implementation does not invent
missing acknowledged history or silently reset the checkpoint. Offline auth
status can drain a valid pending delivery without connecting; on failure it
returns pending-projection status and retains the payload. Read-only commands
continue to avoid opening/upgrading the writer session store.

## Dependency and review boundary

The root module uses a relative source replacement for upstream whatsmeow
`v0.0.0-20261017175727-b2b1e71d45e2`, commit
`b2b1e71d45e2ff171153b025b5a42f475bb30ef8`. The original license, pristine
per-file hashes, explicit companion patch and reconstruction verifier are in
`third_party/whatsmeow-patches`. No private module/cache path is a dependency.
The root test gate does not traverse the companion module; run its documented
tests separately. Native PostgreSQL execution remains unvalidated; the CLI uses
SQLite. An upstream SDK contribution needs that additional validation.

This replaces the disproved synchronous-precommit prototype: App writes no
longer precede SDK cursor/payload commit. Old failed experiments remain research
evidence. It also replaces protocol-version-keyed replay identity, which collided
across replacements. Neither old prototype's passing weak assertions nor marker
presence alone establishes complete repair.

## Synthetic acceptance evidence

Tests use temporary stores and synthetic keys, with SDK EncodePatch, authenticated
decoding and the real collector. The same held-callback/partial-batch fixtures
retain the baseline failure and compare the candidate's persisted state after
actual SIGKILL and a new process. Additional tests cover transaction-internal
crashes, partial writes, checkpoint failure, newer updates, exact replay,
acknowledgment identity, snapshots, peer freshness, cancellation, six IQ outcomes,
uncovered-feature recovery and real CLI offline startup failure/repair.

There is no live WhatsApp activity, production-state test, message-loss claim,
general outgoing-message deduplication proof or deployment acceptance here.
Final source gates and independent review must be attached to the exact final
source snapshot. Remove the source carry once a released upstream SDK passes
these same regressions.
