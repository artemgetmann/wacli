# Standalone agent workflows

The `agent` namespace packages workflow safety with wacli. It needs Node 24+
and FFmpeg/ffprobe for audio; it does not need the OpenClaw executable, gateway,
source tree, or configuration. The Go binary embeds its version-matched helpers.

## Commands

```sh
wacli agent recent-reply --target 15551234567@s.whatsapp.net --json
wacli agent monitor-check --target 15551234567@s.whatsapp.net --state-file /private/monitor.json --json
wacli agent monitor-ack --target 15551234567@s.whatsapp.net --chat 15551234567@s.whatsapp.net --id SNAPSHOT_ID --ack-token RETURNED_TOKEN --state-file /private/monitor.json --json
wacli agent health --json
wacli agent live status --json
wacli agent live ensure --json
wacli agent send-text --to 15551234567@s.whatsapp.net --message-file /private/approved.txt --reply-to ORIGINAL_ID --json
wacli agent send-file --to 15551234567@s.whatsapp.net --file /private/document.pdf --caption-file /private/approved-caption.txt --json
wacli agent transcribe --file /private/downloaded-audio.oga --json
```

Root `--store`/`--account` selects the same store for all helpers. Send, health
and live also accept their existing `--store` helper flag; read helpers retain
`--db`. Existing monitor baselines passed with `--state-file` remain intact.
The default monitor state is under the selected wacli store, not a framework.

`monitor-check` and `recent-reply --state-file` read the baseline without changing
it. A new-message result includes an acknowledgement token bound to its exact
target, message, database, state file and previous baseline. Only after all
required processing, durable result storage and notification succeed, use
`monitor-ack` with that snapshot. A transcription failure, blocked item or failed
notification stays pending. Concurrent stale tokens cannot overwrite a newer
acknowledgement; newer messages arriving after the check remain for the next
poll. Acknowledgement changes only the monitor baseline, not WhatsApp read state.

Read/draft operations do not mark messages read. Draft from supplied context;
read the exact conversation only when requested or during approved send
preparation. `recent-reply --refresh` is explicit stale-history recovery, not
the default. Refresh serializes with sends and returns individual stage timings.
Current-run connection evidence is distinct from caught-up message history.
Owner `sendReady` separately verifies a responding local IPC server belonging to
the matched process. Safe sends wait within the existing owner startup limit
before dispatching; an unready owner means nothing was sent. IPC binds before
unrelated app-state recovery, and invalid long socket paths fail setup early.

## Sends and pending work

The safe path requests upstream JSON receipts, dispatches once, and reconciles
the exact chat/message ID against read-only history. Attachments require a
matching plaintext hash, media type and caption; caption alone is not proof.
With no sync owner, it sends using a short-lived connection. With a known running
owner, it waits for readiness and delegates once. If readiness times out or ownership
is unclear, the preflight refuses the send. Sending never starts or restarts background sync.

Agent text replies require the ID of the last incoming message shown to the user
when they approved the reply, even if they chose to answer only some questions.
Omitting that ID stops the reply before sending. For proactive messages or an
explicit request to leave unread alone, select `--read-policy preserve`.

```sh
wacli agent send-text --to 15551234567@s.whatsapp.net --message-file /private/approved.txt --read-policy handled --handled-through INBOUND_ID --json
```

For text, the same owner captures known pending message IDs before sending,
then dispatches only those IDs using explicit private `read-self` after send
acceptance and local save. It never uses ordinary receipts or app-state recovery.
The `reply` policy accepts the same explicit boundary; without it the agent refuses to send. Raw text exposes `--handled-through ID` and `--preserve-unread`.
The new `text_handled_reply` delegate kind makes older owners reject before
sending instead of ignoring scope. File sends retain their preserve default.

App migration36 tracks exact new live unread membership and durable attempt
guards. Legacy counts, manual markers, conflicting projections and ambiguous
history defer acknowledgement; they are never reconstructed from a history
suffix. A known read/zero baseline followed by new live input can establish
membership. Captured IDs exclude later arrivals even when timestamps are older.
Receipt and app-state echoes of private attempts cannot broaden local settlement
after partial failure, restart or phone/LID reconciliation. Ambiguous echoed
reads can conservatively leave the CLI pending even if another device was read.
Explicit user mark-read commands remain available.

`local_read` and agent `readState` separate `dispatched`, `uncertain`, `deferred`
and local-settlement outcomes. Dispatch proves a socket write, not server
acceptance or a Desktop/iPhone badge change. A durable attempt is never retried,
including after restart; completed old IDs are skipped when an explicit private
read progresses to new input. Never resend the accepted reply to repair read state.
Device behavior and preservation of concurrent remote unread require controlled
live acceptance. `device_verified` remains false until separately observed.
See the [matched iPhone unread test](matched-iphone-unread.md) for the live
comparison of exact message IDs and a read-through boundary.

JSON keeps transport acceptance (`send.accepted`), `verification`, `quote`,
and `readState` separate. `verified_local_metadata` does not prove recipient
quote rendering. A timeout after dispatch can still be followed by a send.
Never resend to repair local persistence, receipt parsing, a quote, or read state.
Failures with a possibly dispatched message need exact reconciliation first.

## Audio provider

Retrieve exact media first with `wacli --read-only media download --chat JID
--id ID --output PATH --json`. Verified saved copies work while sync owns the
store. After HTTP 403, 404 or 410, explicitly recover the same message with
`wacli media download --chat JID --id ID --output PATH --retry-on-phone --json`.
This delegates to the running owner without stopping sync. Never substitute a
date-filtered batch retry or another recording; no response is not deletion.

`transcribe` preserves the original, probes the audio stream, normalizes Opus
and unsupported/extensionless formats to bounded MP3 chunks, and retains MP3/WAV
fast paths. Provider errors and an empty successful transcript are separate.
It does not silently switch providers or ask the phone to retry a batch of media.

The first standalone adapter uses OpenAI with the configured model, defaulting
to `gpt-4o-mini-transcribe`. Supply `OPENAI_API_KEY` in the process environment,
or provision a credential once using stdin:

```sh
wacli agent configure-transcription --model gpt-4o-mini-transcribe
```

The key is read from stdin; never put it in argv or logs. Provisioning creates
`agent-transcription.json` with mode 0600 and refuses to replace an existing
profile. On macOS, new provisioning defaults to Keychain: the profile contains
only provider/model and the Keychain service/account reference. Other platforms
retain explicit owner-only profile storage. No other project's config is read.

Migrate an existing selected-store plaintext profile on macOS with
`wacli agent configure-transcription --credential-store keychain --migrate-profile`.
This reads that profile without stdin, preserves its provider/model, provisions
without blindly replacing an existing Keychain item, verifies exact readback,
then atomically replaces the profile with the reference. Errors preserve the
working profile and never expose native-tool credential output. Test actual
transcription in the agent's login environment before declaring migration
complete. Keychain, profiles and all private data stay outside Git.

Provider contract: [file transcription](https://developers.openai.com/api/docs/guides/speech-to-text)
and [transcription API](https://developers.openai.com/api/reference/resources/audio/subresources/transcriptions/methods/create).

## Rollout and rollback

Build and run focused tests before replacing an installation. A running owner
retains its old executable until restarted. Preserve the previous executable,
startup configuration and a consistent two-database store backup. Git rollback
cannot unsend a message or undo WhatsApp account state.

Some legacy filenames (`openclaw-sync-owner.json`, `openclaw-sync.log`, and the
per-store operation lock) remain for safe migration; they are not dependencies.
Do not start two owners or clear a live lock merely because a filename is old.
Run owner management through the installed `wacli agent live` commands. Retired
standalone helper scripts can share these filenames while failing to recognize
the current sync command; they can overwrite a healthy owner's metadata with
the PID of a competing process that subsequently fails to acquire the lock.
Do not run archived helpers against an active store. A dead recorded owner with
a live lock holder is an ownership conflict: preserve the lock and establish
the live process's exact command, IPC identity and original run evidence before
repairing metadata. Never create new connection evidence merely to pass health.

Only a definite process-not-found result establishes absence. Permission denial,
failed process inspection and an unavailable inspector leave identity unknown;
`live ensure` and `live stop` preserve existing ownership evidence in that state.
Stopping an owner retains the `LOCK` pathname and inode: kernel locks release on
process exit, while unlinking the file could allow competing locks on different
inodes. The offline macOS acceptance fixture (`node scripts/test-owner-os.mjs`)
uses disposable processes, a real kernel lock and a ping-only socket; it never
opens an account database or connects to WhatsApp.
