# Commands and provenance

## Historical before/after fixture

Baseline production source: `19d1d0715c99f236d07dd5b1d9bbad0eb7a70f50`, with
the regression test added locally. Candidate source:
`11be29bf42e729731de5dbece6824f8456a02a47`, tree
`e5d92bba12338b9ea487bf467a00e57a53dea35d`.

The baseline log contains the full TestGroupedReceiptMessageIDAliasContract
run and a FAIL verdict. Its exact historical shell invocation/environment was
not saved in that log. This command is a reproducible minimal equivalent,
NOT an asserted historical command:

```sh
go test ./internal/app -run '^TestGroupedReceiptMessageIDAliasContract/reported_message_id$' -count=1 -v
```

To reproduce the baseline, use a disposable checkout at the baseline revision
and add only `internal/app/receipt_message_id_alias_test.go` from the candidate
revision. Do not replace baseline production parser code. All test values and
stores are synthetic. No transport is connected or ACK sent by this fixture.

The contemporaneous proposal checkpoint records these candidate command forms
and regex (the targeted logs do not preserve shell/environment headers):

```sh
go test ./internal/app -run 'TestGroupedReceiptMessageIDAliasContract|TestOutgoingReceiptValidation|TestOutgoingReceiptSyncMarkers|TestOfflineCompletionFreshnessRecorded' -count=1 -v
go test -tags sqlite_fts5 ./internal/app -run 'TestGroupedReceiptMessageIDAliasContract|TestOutgoingReceiptValidation|TestOutgoingReceiptSyncMarkers|TestOfflineCompletionFreshnessRecorded' -count=1 -v
go test -race ./internal/app -run '^TestGroupedReceiptMessageIDAliasContract$' -count=1 -v
```

The post-fix logs show PASS; successful assertion values are not printed.
Read the checked-in test to see the asserted correct ID, target-only delivery
status, failure flag and successful-sync milestone. The baseline also fails a
whitespace-alias exact-value assertion; both versions still reject that invalid
case. The decisive valid-ID failure is `reported_message_id`.

## Full gate and SDK

`full-gate/results.json` retains the actual recorded argument arrays, exit
codes and durations at the tested source commit. Only the private absolute
toolchain directory is replaced with `<GO_TOOLCHAIN>`. The local pnpm wrapper
failed with ENOEXEC, so recorded direct script equivalents were used. Do not
interpret this as proof that the wrapper itself worked.

The retained SDK logs have no command header. The execution handoff records:

```sh
GOTOOLCHAIN=go1.27.3 go -C third_party/whatsmeow test -json -modfile=../whatsmeow-patches/test.mod -run '^Test' -count=1 ./...
GOTOOLCHAIN=go1.27.3 go -C third_party/whatsmeow test -race -modfile=../whatsmeow-patches/test.mod -run '^Test' -count=1 .
```

A private workspace GOCACHE was used; its path is omitted. The SDK `Test` filter
excludes examples; no account example is claimed. The SDK suite's verdict ledger
and exact retained race output are included.

## Provenance

The original preparation checkpoint records SDK reconstruction verification,
but no separate raw historical verifier-output file was retained. A newly
executed read-only reconstruction verification is published in
`provenance/recheck.log`, explicitly dated at publication and run against the
unchanged tested source. Its normalized command is:

```sh
python3 third_party/whatsmeow-patches/verify.py --upstream-dir <PINNED_PRISTINE_MODULE_DIR>
```

This new verification is not a rerun of the test suites or live diagnostic.
