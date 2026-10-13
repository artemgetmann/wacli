# Standalone WhatsApp workflow

Keep the upstream WhatsApp CLI usable by a human or agent without an agent
framework. Fix observed personal workflow failures with the smallest complete
change: exact media access, confirmed send identity, native quotes, preservation
of pending work, and accurate freshness/connection evidence.

Upstream owns WhatsApp protocol mechanics and the message store. The agent
namespace owns workflow input, reconciliation and separate outcomes. Helpers
ship with the binary; credentials, contacts, recordings and account state do not.

No new messaging platform, automatic correspondence, full provider framework,
or general replacement for OpenClaw. Retain upstream raw commands and remove
carried patches when released upstream behavior passes their regressions.

Success means the installed common workflow passes controlled live acceptance,
not just that source tests pass. See [architecture](docs/architecture/overview.md)
and the [agent contract](docs/agent.md).
