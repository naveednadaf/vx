# vx — local xray proxy manager

A lightweight CLI daemon and Chrome MV3 extension for routing browser traffic through xray-core.

Licensed under the [MIT License](LICENSE).

**Design principle:** 127.0.0.1 loopback only — **never system-wide**. The system proxy is untouched; traffic routes only when a browser tab or CLI command opts in.

---

## 1. Quick start

Build (requires [Go 1.26+](https://go.dev) and xray-core installed as `xray` on your PATH):

```bash
go build -o bin/vx ./cmd/vx
# start the daemon (Ctrl-C to stop)
./bin/vx serve
```

Outputs:
- **Control API:** `http://127.0.0.1:18082` (talks to the Chrome extension)
- **HTTP proxy:** `http://127.0.0.1:18080` (browser-only / per-request)
- **SOCKS5 proxy:** `socks5://127.0.0.1:18081`

Then add your own nodes (see §3) — e.g. `./bin/vx add default my-nodes.txt` with one `vless://` link per line.

---

## 2. Install the Chrome Extension

1. Extract the bundled extension anywhere: `./bin/vx ext install ~/vx-extension`
2. Open Chrome and navigate to `chrome://extensions`
3. Enable **Developer mode** (top-right toggle)
4. Click **Load unpacked**
5. Select `~/vx-extension`

### Features in the Chrome extension:
- **Master toggle:** enables/disables the local proxy for Chrome.
- **This tab toggle:** turn the proxy ON or OFF for the currently active tab (per-tab routing via tab activation).
- **Modes:**
  - **Lowest ping:** uses xray's `observatory` + `leastPing` balancer to auto-route to the fastest node.
  - **Balance:** round-robin distributes connections across the group.
  - **Direct:** proxy bypasses through direct freedom outbound.
  - **Click any node:** pins that node immediately (manual mode).
- **Group switcher:** switch between node groups instantly.
- **Live ping list:** visual latency bars; click **Ping** to probe nodes on demand.
- **Settings page (⚙):**
  - Add groups from pasted `vless://` links, base64 blobs, or subscription URLs (`https://...`).
  - Delete groups.
  - One-click **Refresh subscriptions** to pull fresh links from remote providers.

---

## 3. CLI usage (per-request proxying)

Run any single command through the proxy without touching anything else:

```bash
# xray starts automatically if the daemon isn't running, and stops when the command finishes
./bin/vx exec -- curl -s https://api.ipify.org?format=json
# returns: {"ip":"13.215.218.33"}

# Or with git:
./bin/vx exec -- git clone https://github.com/...
```

### Managing groups and nodes from the CLI:

```bash
# Add nodes from a file
./bin/vx add gcp /path/to/urls.txt

# Add nodes from a subscription URL (auto-decodes base64)
./bin/vx add sub https://example.com/sub/token

# Pipe links directly
cat links.txt | ./bin/vx add mygroup

# List groups
./bin/vx groups

# Switch active group
./bin/vx use gcp

# Switch mode
./bin/vx mode urltest   # lowest ping
./bin/vx mode balance   # round-robin
./bin/vx mode direct    # direct bypass

# Pin a specific node by index or name
./bin/vx use-node 0
./bin/vx use-node Singapore

# Probe latencies
./bin/vx ping
./bin/vx ping gcp

# Show status
./bin/vx status

# Re-fetch all subscriptions
./bin/vx refresh

# Stop background daemon
./bin/vx stop
```

---

## 4. Architecture

```
                 Chrome Extension (MV3)
              ┌───────────────────────────┐
              │  Popup: lowest/bal/nodes  │
              │  Options: sub URL / groups│
              │  Per-tab proxy toggle     │
              └─────────────┬─────────────┘
                            │ REST :18082
                            ▼
CLI / vx exec ──► [ vx daemon / Go ]
(per-request)               │ supervises (run -c)
                            ▼
                    [ xray-core ]  (:18080 HTTP / :18081 SOCKS)
                     ├── observatory (1m probe) -> leastPing
                     ├── balancer (round-robin)
                     └── REALITY / VLESS outbounds
```

- **Config storage:** `~/.vx/config.json`
- **Generated xray config:** `~/.vx/xray.json`
- **Xray log:** `~/.vx/xray.log`
