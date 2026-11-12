# Maintenance findings and regression checklist

This is the maintained public engineering record for repository maintenance.
Update a finding in place by its upstream issue/PR number; keep its observation
date, proof gap and next action. Private run outcomes and notification history
remain separate. This document grants no implementation or rollout authority;
follow [the maintenance runbook](maintenance.md).

## Findings

Public source status below was checked on **2026-11-12**. Retained assessment dates
identify older evidence, not fresh fork tests or installed acceptance. Before
acting, refresh the source, fork ancestry, equivalent PRs and active ownership.

| Source | Dated assessment and proof gap | Next action |
| --- | --- | --- |
| Ordinary app-state recovery (`regular_low`) | Retained October 15 observation: an ordinary mark-read/mark-unread operation timed out while waiting for app-state recovery after an integrity mismatch. Cause, current reproducibility and repair remain unverified. This is a distinct obligation from the #501/#502 archive-upgrade assessment below; source or history-only tests do not establish ordinary live recovery. | Investigate sanitized recovery phase timings and reproduce offline before proposing a repair. Bring the evidence and scoped repair plan for review; do not infer that longer waits, restart, relinking or clearing state are appropriate. No live experiment or rollout is authorized by this entry. |
| [Archive recovery #501](https://github.com/openclaw/wacli/issues/501), [candidate #502](https://github.com/openclaw/wacli/pull/502) | Both remain open; candidate head `bf975064b71628dfb83f0dcb3c0450472e8d1b04`. The candidate's November 7 update removes incremental fallback settlement: settlement requires a complete snapshot. Earlier incremental-settlement proof does not apply to that head. The retained November 10 fork assessment is still pending. | Assess the exact candidate in isolation: upgrade convergence, incomplete/failed snapshots, unknown preference, preserved archives, replay debt and rollback. Do not infer safe adoption or open duplicate work from the candidate's existence. |
| [M4A MIME #549](https://github.com/openclaw/wacli/pull/549) | Merged upstream; included in [v0.26.0](https://github.com/openclaw/wacli/releases/tag/v0.26.0). Retained November 9 assessment has no exact-head fork baseline/candidate proof. | Check fork applicability, then compare synthetic system MIME mappings, extension case and explicit MIME override. Upstream release is not fork or device acceptance. |
| [Broadcast names #550](https://github.com/openclaw/wacli/pull/550) | Merged upstream; included in [v0.26.0](https://github.com/openclaw/wacli/releases/tag/v0.26.0). Retained November 9 assessment has no exact-head fork baseline/candidate proof. | Check fork applicability; test local-contact precedence, push-name/JID fallbacks and absence of a broadcast group-info request with a disconnected synthetic session. |
| [Message state #492](https://github.com/openclaw/wacli/pull/492) | Public PR is closed. The retained November 10 assessment leaves the distinct whitespace-only placeholder classification/retry regression unverified. Related exact-text/unread fixes do not establish this case. | Reproduce against current source and reconcile equivalent work before proposing anything; preserve intentionally empty edits and protocol-placeholder replay behavior. |
| [Durable recovery #382](https://github.com/openclaw/wacli/pull/382) | Merged upstream. Its merge `dfef73cc0f724b5eefe4946417f52496ce466cca` is an ancestor of fork baseline `6d7f3a0225ad7eda684f66970b8cb0720fa303fd` (checked November 12). The missing-upstream-update question is settled for that baseline; this is not proof of every persistence path. | Do not propose another inclusion PR. Diagnose a distinct persistence defect against its own reproduction and exact source. |

The v0.26.0 replacement assessment remains incomplete until all applicable carried
regressions below pass against the released candidate. The public release notes
and upstream tests do not establish fork equivalence or installed adoption.

## Carried regression checklist

These are proof obligations before retiring a patch, not claims of present
acceptance. The original [#482–#488](https://github.com/openclaw/wacli/pull/492)
contributions are closed individually; their public integration reference is
#492. Reconcile actual released source rather than treating their closure as
proof of inclusion or equivalence.

| Capability and public source | Required regression |
| --- | --- |
| [Uncertain sends #483](https://github.com/openclaw/wacli/pull/483) | Possible transmission followed by disconnect, timeout or cancellation never starts a fresh message operation; known-safe lookup/presence retries still work. |
| [Exact stored content #484](https://github.com/openclaw/wacli/pull/484) | Original, caption, edit and upsert preserve whitespace, Unicode, CRLF and explicit empty text through show/list/search/export JSON. |
| [Native quote identity #485](https://github.com/openclaw/wacli/pull/485) | Text/file/direct/delegated persistence retains accepted quote context and canonical known identity without altering the wire quote; unknown mappings remain unknown; save warnings never resend. |
| [Attachment recipient #486](https://github.com/openclaw/wacli/pull/486) | Canonical storage identity preserves the wire target and accepted ID/hash/length; unknown mappings remain unknown. |
| [Decoded unread #487](https://github.com/openclaw/wacli/pull/487) | First decoded content increments once; empty/protocol/content replays cannot clobber content or resurrect cleared unread; same-second message IDs remain distinct. |
| [Handled boundary #488](https://github.com/openclaw/wacli/pull/488) | Validate readable inbound cutoff before network work; later/same-second arrivals survive completion, echoes and recovery replay. Cover private/default and opt-in receipts, stale-owner rejection, partial failures and incomplete history. |
| [Expired media #482](https://github.com/openclaw/wacli/pull/482) | Verify cache first; explicitly requested exact recovery preserves deadline, unread state and sync ownership. Preserve the accepted media source. |
| [Pending marker #4](https://github.com/artemgetmann/wacli/pull/4) | An older private cutoff preserves a newer pending suffix, including count-zero marker-only state; fully handled latest cutoff clears it. Zero receipt dispatch is not device acknowledgement. |
| [Standalone workflows](agent.md) | Preserve PID-bound owner/IPC readiness, paths with spaces, exact send reconciliation, monitor snapshot/ack crash/concurrency behavior, credential-backed transcription and store/settings compatibility. |

Retire a patch only after applicable isolated equivalence checks and separately
authorized installed/device acceptance with rollback. Retain the smallest
necessary fork delta. Do not broaden this checklist into a new runtime framework,
provider migration or live-account experiment.
