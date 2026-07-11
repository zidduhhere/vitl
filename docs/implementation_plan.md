# VitalLink — Implementation Plan

## 1. Stack Decisions

| Component                          | Language/Tech                                                                            | Why                                                                                                                                                                                                                       |
| ---------------------------------- | ---------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Field Client**                   | Go                                                                                       | Compiles to a single static binary (no runtime deps), cross-compiles trivially to ARM for low-power SBCs, native low-level UDP socket control, goroutines make send/ACK/retry logic clean                                 |
| **Server**                         | Go                                                                                       | Same binary/runtime benefits; more importantly, using Go for both client and server lets you **share the protocol package directly** — one packet definition, zero risk of client/server format drift under time pressure |
| **Doctor Dashboard**               | HTML + vanilla JS + CSS, native `WebSocket` API, Chart.js via CDN for live vitals graphs | No build step (no npm/webpack) means zero setup time and instant iteration — critical for 24h. React adds tooling overhead you don't need for a single-page dashboard                                                     |
| **Baseline comparison client**     | Go (reuse `net/http`) or a quick Python script                                           | Doesn't need to be good — it exists to visibly fail. Go keeps the whole repo one language; Python is fine if you want it done in 10 minutes and don't care about elegance                                                 |
| **EHR storage**                    | SQLite via `modernc.org/sqlite` (pure Go driver)                                         | Zero external DB setup, file-based, and the pure-Go driver avoids cgo/compiler headaches that can eat an hour you don't have                                                                                              |
| **Server ↔ Doctor transport**      | `gorilla/websocket`                                                                      | Mature, well-documented, minimal boilerplate for a WS server in Go                                                                                                                                                        |
| **Network degradation simulation** | Linux `tc`/`netem` (shell scripts)                                                       | Standard, reliable, well-documented loss/bandwidth injection — requires Linux or WSL2 if you're on Windows, confirm your dev/demo machine now                                                                             |

**One-language core principle:** Go for field-client + server keeps your critical path (the actual constraint-compliance work) in a single language with shared code. Only the dashboard breaks pattern, and that's deliberate — it's UI, not protocol logic.

---

## 2. Project Structure

Single Go module using the `cmd/` + `internal/` pattern, so field-client and server import the exact same protocol code — this matters, because a client/server packet-format mismatch is the kind of bug that eats hours right before a demo.

```
vitallink/
├── go.mod
├── cmd/
│   ├── field-client/
│   │   └── main.go            # field worker binary entrypoint
│   ├── server/
│   │   └── main.go            # server entrypoint (UDP listener + WS hub + EHR)
│   └── baseline-client/
│       └── main.go            # naive HTTP client, for demo contrast only
├── internal/
│   ├── protocol/
│   │   ├── packet.go          # packet structs, binary encode/decode
│   │   └── codes.go           # packet type constants (SESSION_INIT, VITALS, etc.)
│   ├── transport/
│   │   └── udp.go             # shared send/ACK/retry (ARQ) logic — used by both client & server
│   ├── session/
│   │   └── manager.go         # session token generation, in-memory session state map
│   └── ehr/
│       ├── store.go           # SQLite access layer
│       └── seed.sql           # dummy patient records
├── dashboard/
│   ├── index.html
│   ├── app.js                 # WebSocket client, live vitals chart, EHR panel
│   └── style.css
├── network-sim/
│   ├── apply_netem.sh         # inject loss + bandwidth cap
│   └── reset_netem.sh         # clear netem rules
└── README.md                  # includes the "why this architecture" assumptions from earlier
```

---

## 3. Implementation Phases (mapped to your 24h window)

**Phase 0 — Scaffold (Hour 0–1)**

- `go mod init`, create the full directory tree, git init
- Stub empty `main.go` in each `cmd/` binary so the module compiles from minute one

**Phase 1 — Protocol package (Hour 1–3)**

- `internal/protocol/codes.go`: all packet type constants from the technical map
- `internal/protocol/packet.go`: structs for each packet type + `Encode()`/`Decode()` using `encoding/binary`
- Write this once, correctly — every other component depends on it

**Phase 2 — Raw UDP send/receive (Hour 3–6)**

- `internal/transport/udp.go`: basic socket open, send, receive — no reliability logic yet
- Get `field-client` sending `SESSION_INIT` and `server` echoing `SESSION_ACK` over plain UDP, zero packet loss, before adding any complexity
- This is your "does the plumbing work at all" checkpoint — don't skip it

**Phase 3 — ARQ + retry logic (Hour 6–10)**

- Extend `transport/udp.go`: sequence tracking, timeout-based retransmission, dedup on receipt
- This is the core engineering deliverable — budget real time here, don't rush it
- Test under `netem` as soon as basic ARQ works, not at the end

**Phase 4 — Session + EHR (Hour 10–12)**

- `internal/session/manager.go`: token generation, session state (map keyed by token)
- `internal/ehr/store.go` + `seed.sql`: SQLite setup, 3–5 dummy patients
- Wire `SESSION_INIT` → EHR lookup → `EHR_PUSH` to doctor path

**Phase 5 — Doctor WebSocket hub (Hour 12–14)**

- `cmd/server/main.go`: WS server accepting doctor dashboard connections
- Relay `EHR_PUSH` and incoming `VITALS` to connected doctor clients as JSON over WS

**Phase 6 — Doctor dashboard (Hour 14–16)**

- `dashboard/index.html` + `app.js`: WS client, live vitals chart (Chart.js), EHR info panel
- Keep it visually clean — this is what judges actually look at during the demo

**Phase 7 — Baseline client (Hour 16–18)**

- `cmd/baseline-client/main.go`: plain HTTP POST of the same vitals payload as JSON, same target
- Intentionally minimal — it just needs to stall/timeout under the same `netem` conditions

**Phase 8 — Network sim + integration testing (Hour 18–20)**

- `network-sim/apply_netem.sh`: `tc qdisc` rules for 20%+ loss, <64kbps cap
- Full end-to-end run: field-client → server → doctor dashboard, under netem, confirm survival
- Run baseline client side-by-side, confirm it visibly fails — this contrast is your demo's core proof

**Phase 9 — Polish (Hour 20–22, if time allows)**

- Delta-encoding flag logic in `VITALS` packets
- `DOCTOR_MSG` coded instructions
- Skip either without guilt if you're behind — the core survival demo matters more than these

**Phase 10 — Demo prep (Hour 22–24)**

- Record backup demo video
- Finalize pitch script referencing the assumption statement (constrained link = field↔server only)
- Sleep is not in this plan but seriously consider stealing 30 minutes somewhere in here

---

## 4. Key Dependencies

```
go.mod requires:
  github.com/gorilla/websocket
  modernc.org/sqlite
```

No other external dependencies needed — everything else (UDP sockets, binary encoding, HTTP baseline) is Go standard library. Keeping the dependency list this short is itself a small talking point: minimal footprint, easy to audit, fast to build on any machine without dependency hell.

---

## 5. Build Order Priority (if you fall behind schedule)

If Hour 12 arrives and you're behind, cut in this order — latest-cut-first is what you protect:

1. ~~Delta-encoding~~ — cut first, doesn't change the core proof
2. ~~Doctor coded messages~~ — cut second, nice-to-have
3. ~~Dashboard chart polish~~ — simplify to plain text feed if needed, function over form
4. **Never cut: ARQ reliability logic and the live netem demo contrast** — this is the entire point of the submission
