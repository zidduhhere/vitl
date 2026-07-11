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
| **Audio codec**                    | Opus, via the `opusenc` CLI (`opus-tools` package) called with `os/exec`                 | Purpose-built low-bitrate speech codec, works well down to 6–16kbps — fits inside your budget alongside vitals traffic. Shelling out to the CLI avoids cgo bindings and their build headaches                             |
| **Image codec**                    | Standard JPEG via Go's `image/jpeg` (stdlib), downscaled + low quality                   | JPEG XS is a broadcast-grade codec with no practical open-source library for a 24h build — mention it as a stated future-work choice in your pitch, but ship plain JPEG now. Zero dependencies, zero risk                 |

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
│   │   ├── udp.go             # shared send/ACK/retry (ARQ) logic for VITALS/session packets
│   │   └── media_window.go    # sliding-window / selective-repeat logic for chunked media transfers
│   ├── session/
│   │   └── manager.go         # session token generation, in-memory session state map
│   ├── ehr/
│   │   ├── store.go           # SQLite access layer
│   │   └── seed.sql           # dummy patient records
│   └── media/
│       ├── audio.go           # capture/read audio, shell out to opusenc, chunk output
│       ├── image.go           # capture/read image, JPEG encode + downscale, chunk output
│       └── reassembly.go      # server-side chunk reassembly + missing-chunk tracking (bitmap)
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

## 3. Media Payload Handling (Audio + Image)

Audio and image payloads are both bigger than a single safe UDP packet (keep payloads ~480B to avoid IP-layer fragmentation, which makes loss worse). This means media needs **chunking + reassembly**, and it needs a **different reliability scheme** than vitals.

**Why not reuse the vitals ARQ as-is:** vitals uses simple stop-and-wait — send one packet, wait for its ACK, send the next. That's fine for small, infrequent, freshness-over-completeness data. Media is the opposite: you need every chunk to eventually arrive (a JPEG missing 10% of its bytes doesn't render), and stop-and-wait would make a multi-chunk transfer painfully slow under 20%+ loss. Media instead uses a **sliding window with NACK-driven retransmission** — several chunks in flight at once, server reports back only the specific chunk indices it's missing, client retransmits just those.

**New packet types:**

| Field             | Type/Size                   | Description                                                             |
| ----------------- | --------------------------- | ----------------------------------------------------------------------- |
| `packet_type`     | 1B                          | `0x40` = AUDIO_CHUNK / `0x41` = IMAGE_CHUNK / `0x42` = MEDIA_NACK       |
| `session_token`   | 4B                          | Matches active session                                                  |
| `media_id`        | 2B                          | Identifies this specific audio/image transfer                           |
| `chunk_index`     | 2B                          | Position of this chunk within the transfer                              |
| `total_chunks`    | 2B                          | Total chunks expected for this transfer                                 |
| `payload`         | ≤480B                       | Opus frame bytes or JPEG byte slice (chunked) — omitted on `MEDIA_NACK` |
| `missing_indices` | variable, `MEDIA_NACK` only | List of chunk indices the server has not yet received                   |
| `checksum`        | 2B                          | Integrity check                                                         |

**Flow:**

1. Field client captures audio or image, encodes it (`media/audio.go` shells out to `opusenc`; `media/image.go` uses `image/jpeg` with downscale)
2. `media/audio.go` / `media/image.go` splits the encoded bytes into ≤480B chunks, sends them via the sliding-window sender in `transport/media_window.go`
3. Server's `media/reassembly.go` tracks received chunks per `media_id` in a bitmap, reassembles when complete, forwards the finished file to the doctor dashboard over WebSocket
4. If chunks are missing after a short timeout, server sends one `MEDIA_NACK` listing gaps; client retransmits only those chunks

**Demo framing:** at 64kbps with 20% loss, a compressed image thumbnail will take a few real seconds to fully arrive. Don't hide this — show it on stage as the image **progressively reassembling live** under bad network conditions. That's a stronger visual proof than a vitals number ticking on a dashboard.

**Scope guardrail:** if time gets tight, audio is the safer of the two to keep — it's a stronger "impact" story (a field worker describing symptoms by voice on a basic device) and `opusenc` shelling out is genuinely quick to wire up. Image chunking is the one to cut first if you're behind, since JPEG reassembly is more code for a comparatively weaker narrative.

---

## 4. Implementation Phases (mapped to your 24h window)

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

**Phase 9 — Media chunking: audio first, image if time allows (Hour 20–22, stretch)**

- `transport/media_window.go`: sliding-window sender + NACK-driven retransmit
- `media/audio.go`: capture → `opusenc` → chunk → send (build and prove this first)
- `media/reassembly.go`: server-side bitmap tracking + reassembly
- `media/image.go`: JPEG downscale/encode → chunk → send (only if audio is solid and time remains)
- This entire phase is optional relative to the core vitals proof — see Section 6 for what to cut if you're behind schedule

**Phase 9.5 — Polish (if time allows)**

- Delta-encoding flag logic in `VITALS` packets
- `DOCTOR_MSG` coded instructions
- Skip either without guilt if you're behind — the core survival demo matters more than these

**Phase 10 — Demo prep (Hour 22–24)**

- Record backup demo video
- Finalize pitch script referencing the assumption statement (constrained link = field↔server only)
- Sleep is not in this plan but seriously consider stealing 30 minutes somewhere in here

---

## 5. Key Dependencies

```
go.mod requires:
  github.com/gorilla/websocket
  modernc.org/sqlite

system packages required (install before you start):
  opus-tools    # provides the opusenc CLI, used via os/exec
```

`image/jpeg` is Go stdlib, no dependency needed. Confirm `opusenc` is installed and on `PATH` on your build/demo machine in Phase 0 — don't discover it's missing at Hour 20.

Keeping this dependency list short is itself a small talking point: minimal footprint, easy to audit, fast to build on any machine without dependency hell.

---

## 6. Build Order Priority (if you fall behind schedule)

If Hour 12 arrives and you're behind, cut in this order — latest-cut-first is what you protect:

1. ~~Image chunking (`media/image.go`)~~ — cut first, weakest narrative relative to its build cost
2. ~~Audio chunking (`media/audio.go` + sliding window)~~ — cut second; keep if Phase 8 finished early and this is genuinely solid, otherwise drop
3. ~~Delta-encoding~~ — cut third, doesn't change the core proof
4. ~~Doctor coded messages~~ — cut fourth, nice-to-have
5. ~~Dashboard chart polish~~ — simplify to plain text feed if needed, function over form
6. **Never cut: ARQ reliability logic and the live netem demo contrast on VITALS** — this is the entire point of the submission
