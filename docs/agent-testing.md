# Agent testing runbook

Use this before deciding a test needs a real WhatsApp account. This is workflow
guidance, not recipient authorization. [Agent workflows](agent.md) owns command
and proof boundaries; current user instructions and withdrawals always prevail.

## Start offline

Use neighboring Go tests, `internal/app/fake_wa_test.go`, and the Node helper tests.
`pnpm test` runs the ordinary offline suite; live account integration is opt-in
and outside the standard gate. Run the full gate in `AGENTS.md` before a PR.
For focused disposable-store CLI retrieval scenarios, use the command documented
there with an artifact directory outside the repository.

Simulate timeout, unknown outcome, prior acceptance, duplicates, wrong targets,
and conditional no-reply decisions. Count an unsafe dispatch attempt as a failure
even if a later guard blocks it. Verify mock containment technically: intercept
all executable/transport paths that could dispatch, use disposable stores with
no credentials, and assert no unexpected dispatch. Instructions alone are not a
containment boundary. Never run blind live negative cases.

## Decide whether a live check is necessary

A live check is finite and supervised, for a specific behavior offline tests
cannot prove. Existing applicable user permission for their verified own number
or explicitly designated test group can be reused within its scope; do not
repeat setup or ask again merely because the check is live. If permission or
target identity is missing or ambiguous, resolve that before dispatch. A standing
grant does not authorize indefinite loops, unrelated recipients, sensitive
content, or ignoring later withdrawal. Group ownership alone does not authorize
messaging arbitrary members; cached membership is insufficient when other
participants might exist. Verify the current test audience and designation.

Tests do not themselves authorize pairing, credentials, refresh, sync, service
changes, installation, skill replacement, or sharing private data. Use the
existing protected account/store configuration. Do not create a committed
recipient or authorization registry. Keep actual numbers, JIDs, account state,
secrets, payload files and private logs outside Git in owner-only storage.

## One supervised smoke check

1. Record the tested commit, binary path/version and whether it is source,
   embedded helper or installed behavior. Read that binary's `agent --help` and
   [agent workflows](agent.md); do not silently replace the installation.
2. Select the same explicit account/store for every command. Inspect
   `wacli --account ACCOUNT --read-only auth status --json` and verify the linked
   identity against the user's intended account locally. Resolve and verify the
   exact own number or designated test group at runtime; a display name alone
   is insufficient. Keep identifiers private.
3. Check `wacli --account ACCOUNT agent health --json` and
   `wacli --account ACCOUNT agent live status --json`. Confirm the matching owner
   and send readiness. Missing readiness is a precondition failure, not a reason
   to stop/restart services or switch to a raw send.
4. Check recipient, exact payload, applicable context and prior attempts at the
   final dispatch boundary. An unconditional one-time authorized send differs
   from a conditional no-reply action: the latter, even when manual, needs current
   recipient history. Old global `last_synced` alone neither blocks a requested
   read/unconditional send nor proves missing messages. Report coverage honestly.
5. Prepare one short neutral unique text (for example, `wacli smoke TEST_TOKEN`)
   in a private UTF-8 file. Before dispatch, durably record a private attempt
   ledger with request/test token, account, exact recipient, payload reference,
   timestamp and pending outcome. At most one safe-send invocation per test:
   `wacli --account ACCOUNT agent send-text --to EXACT_TARGET --message-file PRIVATE_FILE --read-policy preserve --json`.
   For a verified self-chat text check, include `--allow-self` in this same
   invocation; do not first try without it and then repeat. These uppercase
   arguments are placeholders, not runnable configuration.
6. Save the exact returned message ID and result to the ledger. An attempted
   dispatch, server acceptance (`send.accepted`), local verification, recipient
   receipt and read receipt are distinct evidence. Accepted and stored does not
   establish delivered or read. Verify receipt/device behavior only within the
   authorized scope; do not claim UI unread badge preservation without UI proof.
7. If accepted, previously attempted, or uncertain (including timeout after
   dispatch), stop further dispatch for the same request/recipient. Reconcile
   the exact message ID through read-only history and available receipt evidence;
   body-only matching is insufficient. If no exact ID can be recovered, report
   uncertainty and stop. Never resend to repair parsing, local persistence,
   quotes or read state, and never repeat an already completed self-send.

Do not mutate read state: preserve is the smoke-check policy. No handled
acknowledgement, sender-facing receipts, or manual stop/send/restart workaround.

## Report useful evidence

Report commit/version, test layer (source, helper, synthetic, live, read session,
or installed), command shape with placeholders, offline check results, attempt
count and the strongest observed outcome. Keep private ledger and raw receipts
local; publish only redacted summaries, never private history or identifiers.
State unverified delivery, UI behavior, history coverage and other limits
explicitly. A blocked or uncertain result is useful evidence, not permission to
broaden the test.
