# Repository maintenance

The existing native seven-day task is the maintenance owner. It reads this
runbook and its private authority, performs bounded repository work, and sends
its full result once to the existing relay, which wakes the coordinating dot.
Keep its existing cadence, owner and route. No additional scheduler, coordinator,
state store or separate coding chat is required.

The launcher must name absolute paths to this file, repository `AGENTS.md` and
the private authority. Resolve repository-relative paths below against that
explicit checkout, never the process working directory. The private authority
binds the actual owner, repository, canonical ledger, reviewed policy/consumer
pair and relay. Missing files, incompatible pins or unresolved configuration
mean HOLD; do not discover substitute owners or replay historical prompts.
If required configuration is unavailable, stop before discovery or publication
and retain the full HOLD result in the existing chat, naming the missing proof
and actions not attempted. Relay it only if the authorized destination can be
verified independently; otherwise record `relay unavailable` locally without
guessing a route, initializing a ledger or creating another store.
Private paths, account settings, routing IDs and acceptance receipts stay private.

## One run

1. Establish trusted actual time, source chat/run identity and trigger evidence.
   A manual or Run now invocation is manual evidence, never proof of an unattended
   scheduled run. Inspect the actual native invocation fields and current request:
   a heartbeat envelope or dispatch timestamp alone proves neither scheduled
   origin nor scheduled start. For a scheduled occurrence, respect its evidenced
   start. If the origin/start needed to authorize work cannot be established,
   HOLD discovery and publication and report the missing evidence; do not relabel
   it manual or invent a start. An explicitly authorized manual invocation may
   proceed within that scope, labelled manual. Reconcile active, pending or
   uncertain prior work before
   starting: observe the same occurrence rather than overlap or redispatch it.
   Maintenance has no inherited daily-sweep hour gate. Apply only restrictions
   explicitly assigned to maintenance by the private authority.
2. Read `AGENTS.md`, `VISION.md`, `docs/architecture/overview.md`, this runbook and
   the private authority. Verify the configured policy/consumer versions and
   digests before use. Read the existing canonical ledger within its 64-record /
   262144-byte limits. Preserve initialization/reconciliation markers, stable
   source IDs, original evidenced creation times, owners, unresolved HOLDs and
   uncertain outcomes. Missing or invalid history is unavailable, not empty.
3. Check upstream releases and relevant open/merged changes against actual fork
   capabilities and carried regressions. Record exact URLs/SHAs, release
   provenance, bounded coverage and unexamined areas. Reassess retained
   `deduplication_inventory_incomplete` HOLDs on every normal run, even without
   new publication: reconcile complete relevant open/closed fork PR evidence by
   stable ID, source, branch and equivalent intent. Do not fabricate completeness,
   reopen settled milestones or compete with another owner's unresolved work.
4. For each actionable finding, including decision-only, ancestry, owner-overlap,
   missing-proof and unknown-publication HOLDs, run the policy below with actual
   bounded evidence. Pass its exact draft report and the same stable finding ID
   to the existing private consumer's `prepare` operation as `{item,policy_result}`.
   Append its validated block to the same canonical ledger only for new/material
   evidence or changed dispositions, before publication. Preserve concurrent and
   historical bytes and all budgets; uncertain persistence HOLDs publication.
   Never invoke the sweep collector for this handoff or append synthetic findings.
5. Where existing authority allows, implement a worthwhile small concrete missing
   fix in an isolated checkout. Establish baseline/candidate behavior and the
   exact-head required repository gate. Reconcile authoritative open/closed PRs
   and overlapping owners immediately before publication. After implementation
   and testing, rerun policy and `prepare` with the current exact tested head,
   fresh inventory, actual test evidence, risks and rollout. A pre-implementation
   HOLD or earlier policy result cannot authorize publication. Persist any changed
   validated disposition and verify the current recorded disposition before
   publishing; unchanged blocks need no duplicate append. Missing or uncertain
   proof HOLDs publication. Create a focused draft only in the authorized fork,
   or update an owned matching open draft. Include
   stable ID, source links, before/after, base/head SHAs, commands/results, risks,
   carried-patch impact, rollout/rollback and unresolved acceptance. Reconcile
   closed/merged work and unknown publication before retry; never duplicate it.
   Verify remote URL, repository, base/head and draft state afterward, then append
   the actual outcome through policy plus `prepare`. A policy decision grants no
   authority. Incomplete implementation, tests or independent review stays draft;
   readiness uses the separate promotion gate below. No automatic approval,
   merge, install, release, deployment or upstream publication.
6. Finish in the existing chat with the full evidenced result: source chat/run,
   actual trigger, start/end, checked sources and coverage, findings including
   retained HOLDs, exact source/PR links, changes, owner actions and blockers.
   After every completed run, including quiet, blocked or failed results, send
   that full result once using the private authority's existing relay and supported
   task-message tool. Record the actual submission outcome. Missing or unverified
   destination/configuration means keep the full result in this chat as HOLD
   with `relay unavailable`; do not send until the authorized binding is verified.
   A blocked or uncertain attempt stays unresolved; reconcile that same attempt
   before retrying. Never
   switch routes to evade denial. Duplicate results are reconciled, not resent.
   Relay submission does not prove parent wake, user notification or device
   appearance. The parent applies materiality, history and actual notification
   receipts; quiet runs still reach the relay without routine user alerts.

## Scope and proof

Maintenance may discover, assess carried patches, make bounded isolated fixes and
publish under existing authority; it may not access live accounts, send account
or third-party messages, acknowledge reads, change binary/service/session/lock/
credentials, or perform device/recovery tests. Any original narrowly scoped
mailbox exception must remain explicit in the private authority; no general inbox
handling follows from it. Do not restart closed review loops or retry uncertainty.

Assess replacement releases against applicable carried regressions: sync ownership
and PID-bound IPC; paths with spaces; exact content, recipient and quote;
uncertain-send reconciliation; placeholder/replay unread counts; pending markers
and exact cutoffs including asynchronous echoes; crash/concurrent acknowledgements;
media; transcription credential boundaries; framework independence; and store/
settings compatibility. Release-note silence is not proof of repair. Source
merged, installed binary, live adopted code and accepted behavior are distinct.
Retire a patch only after applicable isolated and separately authorized installed/
device acceptance with rollback. Never force live races or infer adoption from CI.

Retain every actionable disposition and its original identity across wording or
head changes. Notification deduplication and receipts remain with the existing
consumer/parent; PR inventory reconciliation does not clear notification gates.
Record notification intent before an authorized user alert and its actual receipt
afterward, preserving unknown attempts. A ledger block, PR, worker report or relay
submission is not a user-delivery receipt. No new watcher, channel or test alert.

## Portable policy contract v1

Run `node scripts/maintenance-policy.mjs` with one JSON review on stdin. It reads
no files except its own source for the artifact digest; it performs no network,
publication, account, service or state mutation. Node >=24 is the existing repo
requirement. The public `draftDecision(review)` is pure; `report(review)` adds
version and producer identity. Invalid/budget-exceeded review input exits 2.

Required review fields: `finding_id`, `repository`, `publication_outcome`
(`not_attempted`, `verified`, or an uncertainty value which HOLDs),
`pr_inventory_complete`, `existing_prs` (at most 64 objects), `concrete_change`,
`missing_from_fork`, `isolated_tests_passed`, `tested_head` (40 lowercase hex),
`test_evidence` (nonempty string array), `risks`, `rollout`.
Optional blockers: `overlapping_owner`, `decision_only`, `ancestry_pending`.
Matching PR evidence contains repository, finding_id or equivalent_intent=true,
URL, remote_verified, state, draft and owned. Proof flags must be true where
required. Evidence assertions must come from actual checks, not inferred defaults.

Output: `contract_version=1`, `policy_version=1.1.0`, `producer_sha256`,
`finding_id`, `decision` (`hold`, `create_draft`, `update_existing_draft`) and
`reason`. A digest identifies source bytes; it does not authenticate assertions,
prove remote evidence freshness, authorize publication or acknowledge an alert.
Consumers pin the reviewed contract/version/artifact. Missing or incompatible
reports must HOLD, never fall back to another policy implementation.

Consumers retain their own neutral finding-to-alert mapping. The producer must
not import assistant helpers or include private ledger paths, personal routing,
daily memory, privacy windows or notification/history receipts. PR deduplication
belongs here; personal alert deduplication stays with its consumer.

## Separate readiness-promotion operation (policy 1.1.0)

Legacy draft requests and decisions are unchanged. For promotion pass
`{"operation":"promotion","review":{...}}` on stdin. The public pure
`promotionDecision(review)` returns decision/reason; `promotionReport(review)`
adds artifact/version, stable finding ID, PR URL and reviewed head. Output uses
`promotion_decision=mark_ready|hold|already_ready` and `operation=promotion`;
it has no legacy `decision` and must not enter the draft `policy_result` input.
`publication_performed=false` is explicit. Unknown operations/malformed inputs
fail closed. The bounded stdin limit is unchanged.

Keep all existing draft evidence and inventory fields. Promotion additionally
requires a single verified matching open/owned PR with boolean `draft` and exact
`head_sha`; `pr_inventory_complete` and `remote_verified` must be booleans true.
Closed, overlapping, missing, multiple or unowned PRs HOLD. Equivalent intent
deduplication remains active. Unknown publication HOLDs until authoritative
remote reconciliation, including after an uncertain ready attempt.

Supply actual timezone-aware ISO `decision_at` and `pr_inventory_observed_at`
and nonempty `remote_inventory_evidence` references. Inventory must not be
future-dated or over five minutes old. The caller supplies trusted actual time;
this pure report authenticates neither clocks nor assertions. Preserve original
finding creation time separately. Also require:

- `owner_overlap_checked=true`, with no overlapping owner;
- `implementation_complete=true`, `applicable_tests_passed=true`, and the existing
  test evidence, exact tested head, risks and rollout. Applicable tests include
  the repository mandatory gate and required CI;
- `independent_review_passed=true`, nonempty `independent_review_evidence`, and
  `independent_review_head` equal to tested head and current remote head;
- distinct nonempty `implementation_owner` and `independent_reviewer`. The caller
  must verify independence, not invent different labels;
- explicit `unresolved_findings=[]`: no blocking material/actionable findings.
  Accepted limitations and nonblocking commentary are not blocking findings.

These conditions also apply to `already_ready`, which reports verified state
without another transition. Missing proof never defaults to completion. Only
under current publication authority may the owner call supported ready controls
once the gate passes. Recheck remote PR/head immediately before the call and
verify actual `draft=false` with that same head afterward. APIs without atomic
expected-head support still need both reads. If head changed, HOLD handoff and
do not recommend unreviewed code. Unknown outcomes require read-only
reconciliation before retry. A policy result is not a remote mutation receipt.
Only verified ready PRs reach the downstream parent with a recommendation;
merge, installation, release and live acceptance remain separate decisions.

This source changes no current PR state. Review/install the new artifact/version
pin and consumer manifest as one pair; absent/incompatible results HOLD. Keep
existing assistant ledger decisions/collector unchanged: promotion proof stays
in source evidence, while only draft reports enter its prepare mapping. No
personal routing, notification state, framework imports or new stores live here.

## Verification and change management

`node --test scripts/maintenance-policy.test.mjs` runs portable policy cases and
the public CLI. These tests are automatically included by existing
`test:docs` (`scripts/*.test.mjs`), hence the normal full gate. Golden cases were
captured from the prior policy before extraction; they contain synthetic evidence.
No test fixture proves live delivery or publication.

Publish/install a policy change only after review. Consumers must explicitly
review and update their pinned artifact digest and supported version. Deploy the
producer before switching the consumer; unavailable/mismatched dependencies HOLD.
Keep the previous reviewed producer/consumer pair for rollback. Existing findings,
original creation times and pending publication outcomes must survive migration.
This source addition itself changes no running owner, automation or deployment.
