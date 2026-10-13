---
name: wacli
description: Read WhatsApp history, retrieve exact media, and send verified messages through standalone wacli when the user explicitly requests WhatsApp work.
---

# WhatsApp CLI

Use standalone `wacli`; Node 24+ runs helpers embedded in its executable.
Read `message-drafting` for recipient-facing composition and approval behavior.
Draft from sufficient supplied context without inspecting the destination.
Never send without explicit current-chat recipient/payload authority.

- Read an exact conversation: `wacli agent recent-reply --target <phone-or-jid> --json`.
- Monitor only an explicitly scoped target: `wacli agent monitor-check --target <target> --state-file <private-state-path> --json`.
- Ordinary status: `wacli agent health --json` or `wacli agent live status --json`.
- Start a missing owner when needed: `wacli agent live ensure --json`.
- Explicit stale-history recovery: `wacli agent recent-reply --target <target> --refresh --json`. Try an ordinary read first and establish missing/stale content; never add refresh to routine drafts or checks.
- Download exact audio/media with `wacli --read-only media download --chat <jid> --id <id> --output <private-path> --json`; transcribe using `wacli agent transcribe --file <path> --json`.
- Send the approved exact text through `wacli agent send-text --to <exact-phone-or-jid> --message-file <path|-> --json`.
- Send an attachment through `wacli agent send-file --to <exact-phone-or-jid> --file <path> --caption-file <path|-> --json`.
- Native quoting adds `--reply-to <original-id>` and, where required, `--reply-to-sender <jid>` on either safe-send command. Resolve the original during the final exact-conversation check. A specifically required quote that is unsupported must stop before dispatch; never silently replace it with new wording.

Use one narrow final conversation/account/recipient check immediately before an
authorized send; reuse that check within the same dispatch boundary. If new
activity materially changes the approved response, obtain approval for revised
wording. Pass exact payload as a UTF-8 file/stdin; do not interpolate it in shell
code or use escape rewriting.

Reads, summaries, and drafts preserve unread state. Safe-send defaults to
`--read-policy preserve`. Only if all requests through a known inbound snapshot
are resolved, add `--read-policy handled --handled-through <message-id>`.
Partial answers, unavailable voice notes, and new inbound remain pending.
Do not use sender-facing `--receipts` to implement handled state.

Transport acceptance, local verification, visible quote rendering, recipient
delivery, and read-state updates are different results. Report what the receipt
actually proves. Never resend an accepted/uncertain message to repair parsing,
local history, a quote, or read state. Reconcile the exact ID; body alone is not
attachment proof. No manual stop/send/restart loop or direct raw-send fallback.

404/unavailable audio is not a transcript and does not establish its cause.
Never substitute another recording. Desktop is a scoped fallback candidate;
opening conversations can change unread state, so do not silently use it for a
backlog inspection. No automatic inbound handling or autonomous correspondence.

Setup: read `wacli agent --help` and `docs/agent.md`; no OpenClaw dependency.
