# Standalone agent workflows

The `agent` namespace packages workflow safety with wacli. It needs Node 24+
and FFmpeg/ffprobe for audio; it does not need the OpenClaw executable, gateway,
source tree, or configuration. The Go binary embeds its version-matched helpers.

## Commands

```sh
wacli agent recent-reply --target 15551234567@s.whatsapp.net --json
wacli agent monitor-check --target 15551234567@s.whatsapp.net --state-file /private/monitor.json --json
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

Read/draft operations do not mark messages read. Draft from supplied context;
read the exact conversation only when requested or during approved send
preparation. `recent-reply --refresh` is explicit stale-history recovery, not
the default. Refresh serializes with sends and returns individual stage timings.
Current-run connection evidence is distinct from caught-up message history.

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

`transcribe` preserves the original, probes the audio stream, normalizes Opus
and unsupported/extensionless formats to bounded MP3 chunks, and retains MP3/WAV
fast paths. Provider errors and an empty successful transcript are separate.
It does not silently switch providers or ask the phone to retry a batch of media.

The first standalone adapter uses OpenAI with the configured model, defaulting
to `gpt-4o-mini-transcribe`. Supply `OPENAI_API_KEY` in the process environment,
or provision an owner-only profile once using stdin:

```sh
wacli agent configure-transcription --model gpt-4o-mini-transcribe
```

The key is read from stdin; never put it in argv or logs. Provisioning creates
`agent-transcription.json` with mode 0600 under the selected store and refuses
to overwrite an existing profile. No other project config is read at runtime.
The profile and all private data stay outside Git.

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
