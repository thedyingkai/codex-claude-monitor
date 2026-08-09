# Architecture

The recommended Standalone deployment keeps account credentials and quota
collection on the cloud host itself. The same process normalizes provider
results, stores SQLite state and serves the loopback HTTP API; Caddy is the
only internet-facing process.

```text
Codex app-server / hooks --\
                            > quota-monitor standalone + SQLite -- Caddy/HTTPS --> E32R28T
Claude /usage / hooks -----/                 (cloud host)
                                             local firmware dir --streamed OTA--^
```

The compatibility deployment keeps credentials on each Agent host and sends
only normalized quota numbers and opaque task identifiers to a central server:

```text
Codex app-server ----\
Codex hooks ----------> per-user quota-monitor agent --HTTPS--> Go server + SQLite
Claude /usage --------/                                      |
Claude statusLine/hooks                                     v
                                                     E32R28T display
```

## Trust boundaries

- OAuth credentials remain in the native Codex and Claude Code credential
  stores of the OS user running Standalone or Agent. They are never written to
  SQLite or included in an Agent report.
- Agent tokens can write state for exactly one `agentId`. Display tokens can
  only read the aggregated snapshot and published E32R28T firmware.
- The internet-facing process is Caddy. Standalone listens only on
  `127.0.0.1:8787`; the compatibility server is reachable only on the private
  Compose network in its reference deployment.
- The display stores one revocable read-only token in ESP32 NVS. Serial output
  masks secrets and the JSON endpoint returns no task identifiers.
- The phone configuration page is a separate local boundary: it exists only on
  a temporary WPA2 SoftAP, accepts one client and is never routed through the
  cloud API or the device's normal STA address.
- Firmware publication is a local administrator command, not an HTTP upload.
  The server exposes only the manifest and the current versioned binary; the
  device requires same-origin HTTPS, exact length and SHA-256 before booting it.

## Data flow

Standalone polls provider quota sources every 60 seconds and persists its full
state every 15 seconds without an HTTP hop or `agent:write` token. Compatibility
Agents use the same schedule and report to the central server over HTTPS.
Provider polling runs in a separate serial worker;
slow CLI probes cannot block task reports, and overlapping poll cycles are
coalesced rather than run concurrently. Compatibility Agents query Codex and
Claude concurrently by default. Standalone defaults to ordered Codex-then-Claude
queries and releases the reusable Codex helper between them to reduce CLI
memory overlap on constrained hosts. Full-state replacement means a missed Stop hook cannot
create a permanently active task. The server selects the newest provider
observation across agents, while active tasks are deduplicated per agent,
provider and opaque session ID. Standalone task events can only describe CLI
sessions running on the cloud host; local-PC tasks require compatibility Agents.

Quota observations older than five minutes are `stale`. Agents with no report
for 45 seconds are offline and their tasks are not counted. Individual task
records with no event for 15 minutes expire. Missing provider windows remain
JSON `null`; neither the server nor firmware invents a reset value.

The display's network requests run in one worker queue so an HTTPS timeout does
not block LVGL or touch handling. Screen-off polling continues at the greater
of 60 seconds and the configured normal interval. Entering the local portal or
OTA state pauses ordinary snapshot work and forces the backlight on.

## Provider adapters

Codex uses the documented local JSONL protocol exposed by
[`codex app-server`](https://learn.chatgpt.com/docs/app-server). The collector
initializes one long-lived process and calls `account/read` and
`account/rateLimits/read`.

Claude Code is queried with the local `/usage` slash command in non-interactive
mode. A status-line wrapper also captures the structured `rate_limits` object
that Claude supplies after responses. Neither path sends a model prompt.

## Versioning

The current wire contract is `schemaVersion: 1`. Unknown JSON response fields
must be ignored by devices. Reports using another schema version are rejected
with HTTP 400 so that incompatible agents fail visibly.
