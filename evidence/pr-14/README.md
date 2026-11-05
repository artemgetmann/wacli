# PR14 evidence: confirmed parser defect, limited live conclusion

This bundle makes the retained before/after execution evidence and complete
available review report accessible. It is evidence-only: the PR application
head remains `11be29bf42e729731de5dbece6824f8456a02a47` (tree
`e5d92bba12338b9ea487bf467a00e57a53dea35d`). No application code changed here.
Historical checks/review bind to that source commit, not to this later
evidence-only commit. No source-suite or CI claim is made for this evidence
commit. Review the manifest for transformations and provenance gaps.

## Decisive synthetic before/after proof

- [Actual baseline log](before/baseline.log): public `message_id`/absent-`key`
  shape emits `[""]`, sets the failure flag, stores no target delivery status,
  and withholds the successful-sync milestone. Expected FAIL.
- [Actual fixed plain log](after/targeted-plain.log), [FTS log](after/targeted-fts.log)
  and [race log](after/targeted-race.log): same contract passes after the narrow
  absence-only alias. Explicit malformed keys still fail closed; unrelated
  test messages receive no receipt.
- [Exact tested regression source](https://github.com/artemgetmann/wacli/blob/11be29bf42e729731de5dbece6824f8456a02a47/internal/app/receipt_message_id_alias_test.go).
- [Commands and provenance gaps](commands.md): the original baseline shell
  command was not retained. A clearly labelled reproduction command is given;
  no historical command or literal Codex review invocation is fabricated.

The fixture invokes the real SDK parser/group dispatch and App persistence
handler with synthetic values/temp SQLite. It does not connect or test a real
transport ACK. This proves the parser defect on the reported shape, not its
historical occurrence on any account.

## Review and full validation

- [Complete retained independent-agent review](review/independent-agent-review.json)
  and [method/limits](review/method.md). No actionable P0-P3 findings; all five
  changed files reviewed. Internal Codex agent review, not `codex review` CLI,
  formal GitHub approval, external protocol review or independent test execution.
- [Actual full-gate argument arrays/exits/durations](full-gate/results.json),
  [plain verdict ledger](full-gate/plain-results.ndjson),
  [FTS verdict ledger](full-gate/fts-results.ndjson), [counts/skips](counts.json).
  Plain/FTS named test pass events: 2084/2112. Script/agent suites: 79/53.
- [SDK verdict ledger](after/sdk-results.ndjson), [SDK race output](after/sdk-race.log),
  [new read-only provenance recheck](provenance/recheck.log).
- [CI41669463464](https://github.com/artemgetmann/wacli/actions/runs/41669463464)
  and [Docker41669464097](https://github.com/artemgetmann/wacli/actions/runs/41669464097)
  passed on the exact PR source head. Those are historical source checks,
  separate from this evidence publication.

Two optional comparisons lacked previous binaries: TestAppStatePairingFencesPreviousCLI
and TestPreviousPublishedWriterRefusesRetirementSchema. Nine root child helpers
skip standalone and are exercised by parents; the SDK child worker also skips
standalone. No PostgreSQL or live installed-fix validation is claimed.

## Separate live trial, not validation of the fix

[Redacted trial result and follow-up](live/separate-pre-fix-trial.json): the
original parser plus diagnostic emitted no matching record in one <=15-minute
trial. One delivered receipt was later found for that exact test message and
expected peer, dated within the window. Ingestion time and wire ancestry are
unknown. The sink was open, but no live positive control established diagnostic
send/callback correlation. No record is inconclusive. The fix was not installed;
the original service was restored. No historical receipt recovery is established.

## Scope and privacy

PR14 accepts `message_id` only when `key` is absent. Present invalid keys still
reach existing rejection. It never substitutes the outer transport ID, suppresses
invalid callbacks or changes ACK/freshness/schema/dependency rules. Per-user
status-type interpretation is separate. Public evidence:
[WAHA2337](https://github.com/devlikeapro/waha/issues/2337),
[upstream1314](https://github.com/tulir/whatsmeow/issues/1314).

Later public corroboration: another SDK fork committed
[message_id support on November4](https://github.com/devlikeapro/whatsmeow/commit/d47ffbb06dca60b56feb37c7d15596faca519ea4).
That fork also changes per-user type handling and empty-key fallback. It is
separate implementation evidence, not our tested code or an official protocol
specification; PR14 retains its narrower absent-key policy.

The small synthetic logs and complete structured review are published verbatim
after inspection. Large JSON suite logs are reduced to complete verdict ledgers:
unrelated fixture stdout, run records and timestamps are omitted transparently.
Private absolute toolchain paths are normalized. Live evidence is a strict
allowlisted derivative, not a raw archive. No credentials, phone numbers,
account/group/message IDs, private payloads, unrelated machine data, database
backups or diagnostic binary are included. See manifest.json for file hashes,
original source labels, transformations and omissions.

RECEIPT_ALIAS_PROPOSAL.md in the PR records an older checkpoint. Current PR
validation and this evidence index describe the later completed validation.
This publication grants no merge/install authority and changes no live service.
