# Repository maintenance

The existing maintenance task checks upstream changes against this fork's carried
fixes, keeps useful history and proposes worthwhile small draft PRs. It is
repository work, separate from the shipped CLI and account-facing workflows.
Follow `AGENTS.md` for implementation, tests and PR requirements.

## Entry and ownership

Keep the scheduled entry short:

> Read and follow `<checkout>/docs/maintenance.md` and
> `<private-maintenance>/bindings.md` for this maintenance run.

Resolve the placeholders to reviewed absolute paths at installation. Private
bindings identify the existing task/owner, canonical repositories, current finding
register, history, carried-fix references, publication authority and result relay.
Keep those bindings and records outside the public repository. Missing required
bindings mean HOLD with an honest local result; do not guess paths or recipients.

Use the same authority for manual and scheduled runs. Obtain actual time and
record the trigger evidence available. Missing optional scheduled metadata is
unknown, not a new access gate or proof of scheduled execution. Do not inherit the
daily sweep's hours. Reconcile an active, pending or uncertain run with the same
owner before starting; observe that run rather than dispatching another. Preserve
the existing task and cadence. This runbook creates no scheduler or coding owner.

## Discovery and authority

Read the current register and carried-fix references. Check relevant upstream
releases and open/merged changes against current fork source and its carried
regressions. Record what was checked, exact source URLs/SHAs, release provenance
and coverage gaps. Revisit retained findings when relevant new evidence appears;
unchanged HOLDs are not new work. A merge or release note is not proof of fork
inclusion, installed adoption, equivalent behavior or device acceptance.

Before any proposed fix, reconcile authoritative open and closed fork PRs and
active owners by source ID, branch and equivalent intent. Continue only an owned
matching draft; respect other owners, closed/merged outcomes and unresolved
publication attempts. Incomplete inventory or uncertain ancestry means HOLD,
not permission for a duplicate PR. Complete relevant pagination.

Under the current private authority, a concrete small missing fix may be
implemented in isolation and proposed as a focused fork DRAFT PR. Include the
source finding, before/after, exact base/tested head, applicable baseline/candidate
checks, risks and rollout/rollback. Follow AGENTS, obtain independent review and
immediately before creating/updating a draft refresh the PR inventory and owner
evidence, then reconcile the candidate head with the exact tested/reviewed head.
A changed head, new overlap or unavailable evidence means HOLD and renewed checks.
Do not publish using the earlier discovery inventory. After publication,
verify actual remote repository, PR URL, head and draft state.
Missing tests, unresolved ownership or decision-only findings stay visible as
HOLDs; a maintenance run need not produce a PR. No permanent coding-chat handoff
is required. Ready promotion, merge, release, installation and live acceptance
remain separate authorized actions with exact-head evidence.

Maintenance does not access accounts, send account messages/read acknowledgements,
change binaries/services/sessions/locks/credentials or run device experiments.
Preserve separately owned proof work. Retire a carried patch only after equivalent
isolated checks and separately authorized installed/device acceptance with rollback.
Use specific historical evidence when relevant, rather than replaying old prompts
or scanning an inventory of temporary receipts.

## Findings, history and result

Maintain one private current register by stable source ID and obligation. Preserve
original creation times, material facts, owners, prior PR outcomes, receipts and
unresolved/unknown states. Update the existing entry when its material evidence
changes; prose or draft-head changes alone do not create a new obligation. Record
changed dispositions and pending publication before acting, then the verified
outcome. Preserve the previous material state in dated history. Quiet scans need
no fake finding. A missing or inconsistent register is unavailable, never empty.

Keep current entries compact and link to detailed evidence/history only when
needed. Preserve prior records as readable history rather than repeatedly copying
them into the active register. There is no lifetime event/byte cap: more than 64
historical updates must not drop a finding or block maintenance. Reconcile any
active/uncertain owner before writing. All register writers must hold an exclusive
OS lock on the stable `<register>.lock` sibling (never replace that lock file).
Under that lock, compare the register's bytes
or digest with the version read; a mismatch means HOLD and reconciliation. Confirm
the prior material state is saved to history, stage the replacement in the same
directory, recheck the original and replace by atomic rename, then verify the
result. If locking, history preservation or write outcome is uncertain, HOLD and
read back both files before any retry; never overwrite an intervening edit.

Finish every run with its identity, actual timing/trigger evidence, checked
sources, coverage, findings or quiet result, changed files/PRs, owner actions and
limits. Save that full result in private run history. Record the result-submission
intent there, then send the full result once to the existing relay in the private
bindings, including quiet, HOLD and failed runs. Record the actual outcome. If
blocked or uncertain, report locally and reconcile the same attempt before any
retry; do not rerun maintenance to obtain a receipt.

A relay receipt is not parent wake, a user alert or device delivery. The existing
reporting owner selects useful new/material updates using actual notification
history and records intent/receipts. Missing history or unknown sends remain
unresolved; do not invent a clean baseline or notify unchanged findings blindly.

## Compatibility and cutover

The existing `scripts/maintenance-policy.mjs` draft/promotion policy and its
regression tests remain available for legacy callers. The routine flow above
requires neither their version/digest pins nor the downstream prepare/collect
adapter, collector schema, daily-memory helper or old ledger limits. This change
removes no rollback consumer and grants no new publication authority.

Review the private migration and cutover plan before installation. Verify every
existing identity and its original facts/uncertainty against preserved source
history; do not infer resolution during conversion. Switch only the existing
entry after authorization, read back its owner/cadence, and verify the actual run
and result route separately. Retain old configuration/history/consumers for
rollback. Source tests and disposable agent exercises do not prove native or
unattended acceptance. Cross-repository cleanup and old acceptance-checker
retirement require explicit follow-through; they are not executed by this PR.
