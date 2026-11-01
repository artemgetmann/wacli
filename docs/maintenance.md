# Repository maintenance owner

This is developer tooling, separate from the shipped Go CLI and account-facing
agent helpers. The repository maintenance owner owns upstream discovery,
carried-regression review, isolated fixes, draft policy and draft PR evidence.
A personal assistant may consume structured findings; it does not own these rules.
No framework runtime, private account state, scheduler or notification store is
part of this tooling.

## Discovery and authority

Check relevant upstream releases and open/merged changes against the fork's
actual capabilities and carried regressions. Record bounded coverage, exact
source URLs/SHAs, release provenance and unexamined areas. Release-note silence
is not proof of repair. Distinguish merged source, installed binary, live adopted
code and accepted behavior. Connectivity or source tests alone do not prove live
acceptance. Do not access accounts, change services or run live tests merely to
perform maintenance discovery.

Give each actionable finding a stable ID, original evidenced creation time,
source, owner, current disposition, required action and material risk. Keep that
identity across wording changes, reruns and new draft heads. Retain decision-only,
ancestry-pending, owner-held, missing-test and unknown-publication findings.
Record every actionable disposition in the existing authoritative maintenance
record before publication, then append actual verified publication results.
Never create a parallel inventory or erase unresolved history.

Before publishing, inspect the existing inventory AND authoritative open/closed
fork PRs for stable ID, source issue/PR, branch and equivalent intent. Update only
an owned open draft. Preserve another owner's work. Reconcile closed/merged work
and unknown publication before retrying; never create a duplicate on uncertainty.

A focused draft requires a concrete bounded change missing from the fork,
no overlapping owner, isolated baseline/candidate evidence where meaningful,
an exact tested head, tests, risks and rollout/rollback notes. Follow AGENTS.md
and applicable repository skills. Include stable ID, upstream links, before/after,
base/head SHAs, commands/results, carried-patch impact and unresolved acceptance.
Privacy-safe evidence only. Verify actual remote URL, repository, base/head and
draft state after publication. The decision function grants no authority: actual
publication still requires the current operator's authorization. No automatic
approval, ready transition, merge, release, deployment or upstream publication.

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

Output: `contract_version=1`, `policy_version=1.0.0`, `producer_sha256`,
`finding_id`, `decision` (`hold`, `create_draft`, `update_existing_draft`) and
`reason`. A digest identifies source bytes; it does not authenticate assertions,
prove remote evidence freshness, authorize publication or acknowledge an alert.
Consumers pin the reviewed contract/version/artifact. Missing or incompatible
reports must HOLD, never fall back to another policy implementation.

Consumers retain their own neutral finding-to-alert mapping. The producer must
not import assistant helpers or include private ledger paths, personal routing,
daily memory, privacy windows or notification/history receipts. PR deduplication
belongs here; personal alert deduplication stays with its consumer.

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
