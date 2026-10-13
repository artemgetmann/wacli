# Ownership and dependency direction

The Go CLI owns WhatsApp commands, owner delegation and SQLite persistence.
The `agent` Go package embeds Node helpers and executes their version-matched
entry point without a shell. The helpers call this same executable for protocol
operations, inspect local history read-only, and return separate send/read/quote
results. See [agent contract](../agent.md).

- `agent/lib/send.ts`: approved exact input, per-store serialization, one CLI
  dispatch, exact-ID/hash reconciliation. It does not own connection recovery.
- `agent/lib/owner/`: command-matched process ownership and current-run evidence.
  It does not prove a particular message is fresh or recipient delivery.
- `agent/lib/read/`: read-only identity/conversation lookup and explicitly scoped
  monitor baselines. Forced refresh is explicit and returns its stage timings.
- `agent/lib/transcribe.ts`: local-audio inspection, bounded normalization and
  explicit standalone OpenAI profile. No provider guessing or runtime config import.

No imports or execution of the OpenClaw framework. Its name in the retained
upstream Go module namespace or legacy lock filenames is not a dependency.
No user-specific configuration belongs in the public repository.

Read-state boundaries belong in `internal/app` and the owner IPC because a
wrapper precheck cannot prevent new messages arriving during app-state recovery.
Device synchronization remains a distinct acceptance check.

Existing extracted owner/read modules exceeding 500 lines are initial migration
baselines, not a target for unrelated refactoring. New workflow modules should
remain below that review trigger. Safety regressions and an architecture check
protect their public boundary; split only when responsibilities actually diverge.
