# macOS Network Conditions Control Panel — Design

## Problem

`network-sim/apply_netem.sh` and `reset_netem.sh` use Linux's `tc`/`netem`, which
doesn't exist on macOS. There's no way to interactively control packet loss rate
and bandwidth cap on macOS while developing/demoing VitalLink locally.

## Goal

A standalone local web tool that lets a developer set **packet loss %** and
**speed (bandwidth cap, Mbit/s)** via sliders + numeric entry, and apply them to
a real network interface (e.g. `en0`) using macOS's native `dnctl`/`pf`
(dummynet) facilities — the macOS equivalent of `tc`/`netem`.

This is independent of the VitalLink dashboard (`dashboard/`) and the existing
`network-sim/` Linux scripts. It does not modify either.

## Architecture

```
network-sim/macos-control/
  index.html      - slider UI (loss %, speed Mbit/s, interface field, Apply/Reset)
  app.js           - reads slider/input state, POSTs to backend, renders errors
  style.css        - minimal styling, consistent with dashboard/style.css conventions

cmd/netcontrol/
  main.go          - tiny HTTP server: serves the static page, exposes
                     POST /apply and POST /reset
```

`cmd/netcontrol` is a new Go binary, sibling to `cmd/server` and `cmd/field-client`.
It listens on localhost only (e.g. `:8090`) and takes no other dependencies.

## Mechanism

macOS replaced `ipfw` with dummynet-via-`dnctl` (paired with `pf` for traffic
classification). Apply flow:

1. Backend writes a pf ruleset to a temp file, e.g.:
   ```
   dummynet out quick on <iface> all pipe 1
   ```
2. Backend configures the pipe:
   ```
   dnctl pipe 1 config bw <rate>Mbit/s plr <loss/100>
   ```
   (delay is intentionally omitted from this tool — loss + speed only, per scope)
3. Backend loads the ruleset and enables pf:
   ```
   pfctl -f <tmpfile> && pfctl -e
   ```
4. All commands from steps 2–3 run as a single shell invocation wrapped in:
   ```
   osascript -e 'do shell script "..." with administrator privileges'
   ```
   This shows the native macOS password dialog once per Apply click (not per
   drag), per the chosen privilege model — no long-lived sudo helper process.

Reset runs (also via the same `osascript ... with administrator privileges`
wrapper):
```
pfctl -f /etc/pf.conf
dnctl -q flush
```
which restores macOS's default pf ruleset and clears dummynet pipes. Reset is
idempotent — safe to click with nothing applied.

## API

- `POST /apply` — body `{ "interface": "en0", "loss_pct": 20, "rate_mbit": 1 }`.
  Validates `interface` is non-empty, `loss_pct` in [0,100], `rate_mbit` > 0.
  Runs the apply mechanism above. Returns `200` with `{ "ok": true }`, or
  `500` with `{ "ok": false, "error": "<stderr output>" }` on failure (e.g.
  user cancels the admin-privileges dialog, or `dnctl`/`pfctl` errors).
- `POST /reset` — runs the reset mechanism above. Same response shape.

## UI behavior

- Loss % slider (0–100, step 1) and numeric input stay in sync with each other.
- Speed slider (e.g. 0.1–100 Mbit/s, log-ish step) and numeric input stay in sync.
- Interface text field, defaults to `en0`.
- Apply button sends current slider values to `/apply`. Values only take effect
  on click — dragging the slider does not call the backend (avoids repeated
  admin-privileges prompts).
- Reset button calls `/reset`.
- Errors from the backend render in a visible error banner on the page.
- A static note on the page: "For local dev/testing only — this replaces your
  active pf ruleset while applied. Click Reset when done."

## Out of scope

- Delay/latency control (explicitly excluded — loss + speed only).
- Merging with or preserving any pre-existing custom pf rules on the machine.
- Interface auto-detection/validation beyond non-empty check.
- Automated tests exercising the real network stack (not practical for a local
  dev tool) — verified manually via `dnctl -q pipe show`, `pfctl -sr`, and
  before/after `ping`/`curl` checks.
