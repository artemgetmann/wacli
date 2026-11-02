# Pinned SDK companion

The relative module replacement carries the public source of
`go.mau.fi/whatsmeow v0.0.0-20261017175727-b2b1e71d45e2`, upstream commit
`b2b1e71d45e2ff171153b025b5a42f475bb30ef8`, plus one explicit patch.
The SDK's MPL-2.0 license and original notices remain in the source directory.
This is an additive companion for review, not an independently published SDK
release. No private module, absolute dependency path or module-cache edit is used.

`upstream.json` pins the source URL, module checksum and every pristine file hash.
`changes.json` lists all changed SDK files and pins the patch hash. The patch
contains the native one-slot delivery transaction/schema (successor compatibility19, atomic predecessor witness), verified producer
handoff, selective event provenance, cancelable serialized accepted-local path, and isolated
synthetic tests. The private IQ/blob function seams substitute transport in SDK tests;
production uses the unchanged normal IQ implementation.

Given a separately obtained pristine module directory:

```sh
python3 third_party/whatsmeow-patches/verify.py --upstream-dir "$PRISTINE_SDK"
```

Verification checks every pristine hash, applies the patch only to a temporary
copy and compares every reconstructed file against the carried SDK. Regeneration
requires the same verified upstream directory and `regenerate.py`.

The root Go gate does not traverse a nested module. Run the companion explicitly:

```sh
go -C third_party/whatsmeow test -modfile=../whatsmeow-patches/test.mod ./...
go -C third_party/whatsmeow test -race -modfile=../whatsmeow-patches/test.mod .
```

The alternate test module adds only the consuming CLI's already-pinned SQLite
driver for temporary-store tests. It otherwise retains the SDK's upstream module
graph; the CLI's production graph remains selected by the root go.mod/go.sum.
No PostgreSQL server test has been run; SQL portability is not an acceptance
claim. The CLI uses native SQLite transactions. An upstream SDK submission would
need native PostgreSQL validation separately.

Remove the source replacement when an upstream SDK release implements this
contract and passes the same decoder, local-IQ, crash/restart and integration
regressions. Keep wacli-specific projection SQL and unread policy in wacli. No
upstream issue, PR or publication is part of this implementation work.


The successor exposes retained boundary metadata after acknowledgment and a
serialized local pairing validator. It preserves legacy17 payloads and refuses
ambiguous pending predecessors rather than backfilling from the App checkpoint.
The native compatibility fence rejects older18 writers; returning to them is not
an automatic rollback. Existing license/source notices remain unchanged.


Compatibility19 adds one noncredential retired-delivery slot, written atomically
with credential deletion, and a native prior-epoch witness for bounded same-account
replacement admission. It does not reconstruct deleted legacy payloads, provide
old-binary rollback, or expand the covered App projection set. Retired uncovered
work remains an explicit refusal. The App checkpoint stays schema35.
