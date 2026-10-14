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
It uses upstream daemon delegation and does not routinely stop the sync owner.

Default `--read-policy preserve` leaves the conversation pending. Only when all
requests through a known snapshot have been handled, use:

```sh
wacli agent send-text --to 15551234567@s.whatsapp.net --message-file /private/approved.txt --read-policy handled --handled-through INBOUND_ID --json
```

This delegates `chats mark-read --self-only --through-message-id`; wrong/missing IDs fail
instead of selecting whichever message arrived most recently. Newer inbound
must remain pending. Internal `read-self` receipts update your own linked devices
without sending blue-tick read receipts to contacts or changing account privacy
settings. This path does not wait for app-state recovery. Exact device behavior
is a live acceptance check; a successful dispatch alone is not device proof.

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
