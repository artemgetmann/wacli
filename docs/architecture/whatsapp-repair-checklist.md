# Focused connection and handled-reply repair

This is the single checklist for PR #17. Private account evidence stays outside
Git. A checked source test is not an installed-client acceptance result.

## Verified baseline and decisions

- [x] PR baseline `57d3a2c` adds regression coverage and guidance, no runtime fix.
- [x] Process-probe failures are treated as absence; the stop helper can unlink
  the kernel lock pathname. Both require defensive handling.
- [x] Upstream v0.26.1 shares text-send persistence across direct and delegated
  calls and clears local unread for own messages. This is not linked-client proof.
- [x] Keep the current supervisor; replacement remains an unproved design option.
- [x] Preserve reads/drafts and newer unanswered messages. Bind successful-reply
  local clearing to a pre-dispatch inbound boundary, not the send completion time.
- [x] Keep PR draft. No merge, broad install, live owner/lock changes or extra sends.

## Implementation and evidence

- [x] Preserve alive/dead/unknown process observations; only ESRCH proves absence.
- [x] Unknown command identity blocks owner mutations; retain the lock pathname.
- [x] Exercise actual processes/kernel locking in disposable offline fixtures,
  including uncertain identity, legitimate-owner preservation and normal startup.
- [x] Adapt successful text replies in the shared direct/delegated path; keep
  send acceptance, local save and unread outcomes separate; never resend on warning.
- [x] Focused save-failure, preserve, outgoing re-save and newer/same-second tests;
  existing read/draft and failure regressions passed in the full gate.
- [x] Full repository gate passed on the candidate tree (2026-11-16): format,
  vet, deadcode, plain/FTS Go, Windows/cgo, 79 script and 56 agent tests, build,
  docs site and diff check.
- [ ] Independent exact-head review and controlled installed acceptance.

## Controlled live acceptance (not yet performed)

- [ ] Confirm the documented own-account test group and reconcile prior attempts.
- [ ] Establish an unread test message without sending another CLI message.
- [ ] Observe only that group's Desktop list row before and after; never open it
  to observe, expose other chats, or capture the whole unrelated chat list.
- [ ] Arrange handset observation, using user-authenticated iPhone Mirroring if
  available. Desktop evidence is separate from phone evidence.
- [ ] Dispatch the single approved benign reply only once all before/after
  prerequisites and candidate compatibility are established.
- [ ] Reconcile the exact outgoing ID and separately record CLI, Desktop and
  phone outcomes. Unknown badge/delivery state never authorizes a resend.

## Still unproved and next action

The candidate passes `node scripts/test-owner-os.mjs`: actual OS permission
denial, missing inspector, unchanged ownership evidence, real flock exclusion,
responsive IPC and normal stop/start with the same lock inode. The baseline
read-only status falsely reported the denied live process absent; no baseline
mutation was attempted. These are offline fixture results, not account proof.

Focused shared-send tests pass for local clearing, explicit preservation,
same-second later arrival, outgoing re-save and accepted-send/save failure.
Linked-client and production compatibility remain unproved. The documented
own-account group has unread input, but Desktop is not running. Hold the only
approved send until a safe filtered sidebar view and separate phone observation
are ready. Starting an unknown selected chat is not an observation method.

The live owner has not been replaced. A candidate CLI delegates text to that
owner; its old runtime cannot prove this candidate's new automatic local-clear
behavior. A separately authorized controlled candidate-owner test or equivalent
compatibility proof is required before claiming installed acceptance. No broad
install or live owner/lock change is authorized by this checklist.
