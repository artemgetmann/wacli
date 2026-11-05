# Proposed grouped receipt message_id alias — LOCAL ONLY

Base: installed source 19d1d0715c99f236d07dd5b1d9bbad0eb7a70f50. No publication,
merge, installation, live chat, account read, or service change was performed.

## Verified defect and evidence

Public reports https://github.com/devlikeapro/waha/issues/2337 and
https://github.com/tulir/whatsmeow/issues/1314 describe a grouped receipt whose
participants element names the message through message_id instead of key.
The outer receipt ID is separately reported not to match the sent message.
Our reconstructed synthetic structure reproduces empty SDK IDs and App
rejection. Renaming just the input attribute to key makes the same fixture
target persist, establishing the parser dependency without changing production.
This does not establish the original shape behind this account's old warnings.

Current upstream receipt.go is byte-identical to our pinned file (Git blob
8def76ab6e4c72ec9a48fdd17ce608efdfc08056). The external proposed fix
https://github.com/devlikeapro/whatsmeow/pull/5 is open and unmerged at inspected
head f9b771935fdfe6238069753f63bb24ab98c2238b. We do not adopt its raw-node
warning or ID-less callback suppression. Per-user status-type interpretation
reported in https://github.com/tulir/whatsmeow/issues/1270 remains separate.

## Narrow behavior

- Use message_id only if the legacy key attribute is absent.
- Preserve legacy key precedence, including explicit invalid keys. Do not trim
  identifiers, override contradictory keys, or use the outer transport ID.
- Missing, empty, whitespace or wrongly typed selected IDs still reach App's
  existing validation, which retains the failure flag and withholds milestones.
- No callback suppression, raw payload logging, ACK, schema, dependency-pin,
  app-state projection, or existing recovery changes.
- Regenerate the existing explicit SDK patch and changes manifest; keep every
  PR #9 SDK modification intact. Reconstruction against pinned pristine source
  was verified; there is no module-cache editing or wholesale SDK upgrade.

## Observed tests

New TestGroupedReceiptMessageIDAliasContract exercises the real SDK parser,
grouped dispatch, App sync event handler, SQLite receipt writer, failure flag
and milestone recording. Both own identities are nonempty; eligibility is
asserted. Every fixture/store is synthetic and temporary; no connection/ACK.

Ten cases: legacy key; message_id; key precedence; neither; explicit empty,
whitespace or non-string key despite a valid alias; empty, whitespace or
non-string alias. The baseline fails for the reported message_id case (wrong
emitted ID, no target status, failure flag and no milestone) and the whitespace
alias's exact emitted value. All ten pass with the proposal; invalid cases
remain fail-closed and the unrelated synthetic message receives no receipt.

Run regex:
`TestGroupedReceiptMessageIDAliasContract|TestOutgoingReceiptValidation|TestOutgoingReceiptSyncMarkers|TestOfflineCompletionFreshnessRecorded`

- Plain targeted run with that regex: PASS, 4.069 s.
- FTS targeted run with that regex: PASS, 4.212 s.
- Race run of TestGroupedReceiptMessageIDAliasContract: PASS, 1.699 s.
- scripts/toolchain.test.mjs: 3 passed.
- SDK regenerate/verify: exact pinned source + explicit patch verified.

SDK patch SHA256:
34b58e80cc5ce42c14fdcc625ab023c6204710e7e8ee0c5e56340e9d72a3e3a2.

## Limits and next step

This prevents one known prospective ID-decoding failure. It cannot recover
receipts already skipped or prove message-history completeness. It deliberately
does not repair status-broadcast per-user delivery/read/played mapping; no claim
is made that every status-viewer read becomes correct.

The full repository/SDK suites and independent review have not been completed
for this new proposal; it is not deployment-ready. Next: review the narrow
absence-only policy, then complete that validation before any separately
authorized publication/deployment. A normal live test may not produce the
reported shape. Any diagnostic installation/process change or targeted raw
shape capture needs its own approved method and verified targets; permission
to use the user's test group/accounts alone does not authorize those changes.
