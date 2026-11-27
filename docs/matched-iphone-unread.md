# Matched iPhone unread test

Run this opt-in live test to compare the unread badge after marking two exact
messages read and after marking every message through a chosen message. Run
each supported case on the same iPhone, receiving account and direct test chat.

## Set up the comparison

- Pin and record the wacli revision and WAHA version and engine. Run both on
  the same local host with separate, isolated test sessions. GOWS supports
  sending read receipts for exact message IDs.
- Use two approved test accounts. Verify the receiving account on the iPhone
  and in both tools, and resolve the one test chat in each tool. Keep the
  account's read-receipt setting the same throughout. The wacli private receipt
  and WAHA's account-dependent receipt are different operations. Record which
  each tool used.
- Apply an action with only one tool per case. Between cases, return the test
  chat to a clean phone badge while it contains only synthetic unread messages.
  Keep the chat closed on the iPhone from the first arrival through the final
  observation. Do not open it to identify unread messages.

## Run the cases

For every supported case, send five new, distinct synthetic messages from the same
sender. Send A and B first, observe badge **2**, then send C, D, and E in order.
Use case-specific labels and separate arrivals by at least one second. Verify
badge **5** and all five exact inbound matches in the tool's read-only history
before applying the action. Record the badge after a common settling interval
for all cases, such as 30 seconds.

| Tool | Case | Action | Expected badge if the action matches the request |
| --- | --- | --- | --- |
| wacli | Exact C and D | Unsupported by the CLI. Record this without dispatching a different read action. | 5, with no write |
| WAHA GOWS | Exact C and D | Call `POST /api/sendSeen` with the session, test chat, and only the exact IDs for C and D in `messageIds`. | 3 |
| wacli | Through D | Run the command below with D's exact inbound ID. | 1 |
| WAHA GOWS | Through D | Get the exact IDs for A through D and call `POST /api/sendSeen` with those four IDs. Record that the caller selected the prefix. | 1 |

```sh
wacli --store "$TEST_STORE" chats mark-read --chat "$TEST_CHAT_JID" \
  --self-only --through-message-id "$MARKER_D_ID" --json
```

Use [wacli's read command reference](chats.md#read-receipts) and the
[WAHA exact-ID API](https://waha.devlike.pro/docs/how-to/send-messages/#send-seen).
Supply the explicit ID list for both WAHA cases. A whole-chat read would clear E
and invalidate the comparison. If dispatch is uncertain, reconcile that single
attempt against the phone and history before any further write.

## Record the result

For each case, record the tool version, initial count, requested markers,
command or API outcome, local state for the five markers where available, and
final iPhone count. A matching badge verifies how many unread messages remain.
Claim particular messages stayed unread only where per-message evidence
establishes that. Report a mismatch between local history and the iPhone as
observed. Record unavailable commands or inaccessible phone evidence explicitly.
Keep account identifiers, raw logs, and unredacted screenshots outside the
repository.
