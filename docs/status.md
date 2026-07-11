# VitalLink — Implementation Status

Last updated: 2026-07-11

## Done

| Phase (per implementation_plan.md) | Component | State |
|---|---|---|
| 1 | `internal/protocol` | Done. All 11 packet types (SESSION_INIT/ACK, DOCTOR_READY, VITALS/ACK, DOCTOR_MSG, HEARTBEAT, SESSION_END, AUDIO/IMAGE_CHUNK, MEDIA_NACK) with Encode/Decode + checksum. |
| 2–3 | `internal/transport` | Done. `SendWithRetry` (stop-and-wait ARQ, channel-based so it composes with a shared UDP receive loop), `SeqDedup` (per-session sliding window dedup), `MediaSender` (sliding-window + NACK-driven retransmit). |
| 4 | `internal/session` | Done. In-memory token-keyed session map, random collision-free token generation. |
| 4 | `internal/ehr` | Done. SQLite (`modernc.org/sqlite`, pure Go, no cgo) via `go:embed`'d `seed.sql`, 5 dummy patients (ids 1001–1005). |
| 6/9 | `internal/media` | Done. `audio.go` shells out to `opusenc`, `image.go` does stdlib-only JPEG downscale/encode, `reassembly.go` is the server-side chunk bitmap + multiplexed `Reassembler`. |
| 5, 7 | `cmd/server` | Done. UDP listener (session handshake, vitals ACK+relay, media reassembly+NACK, session end), WebSocket hub broadcasting JSON to dashboards, `/vitals` HTTP endpoint for the baseline client, doctor→field relay (DOCTOR_READY/DOCTOR_MSG). |
| — | `cmd/field-client` | Done. Session handshake with retry, simulated vitals random-walk loop (freshness-over-completeness ARQ), optional `-audio-file`/`-image-file` chunked transfer, graceful SIGINT → SESSION_END. |
| 7 | `cmd/baseline-client` | Done. Naive HTTP POST loop, no retry/reliability — the demo contrast case. |
| 8 | `network-sim/` | Done. `apply_netem.sh` / `reset_netem.sh` (Linux `tc qdisc netem`, configurable loss/rate/delay). |
| 6 | `dashboard/` | Done (built separately, outside this backend work — `app.js`/`config.js`/`style.css` landed in commit `5bdcc69`). Field-verified: its message handling (`session_status`, `ehr_push`, `vitals`, `media`) matches `cmd/server/messages.go`'s JSON schema exactly, no changes needed on either side. |
| — | `go.mod` / `go.sum` | Done. Added `github.com/gorilla/websocket`, `modernc.org/sqlite`. |
| — | `mobile/vitallink` | Done. gomobile-safe Go package (`mobile/vitallink/vitallink.go`) exposing `Client`/`Listener` API over the existing `internal/protocol` + `internal/transport` internals. Verified: `go build ./mobile/... && go vet ./mobile/...` pass clean. |
| — | `mobile/build_android.sh` | Done. Scripts the full gomobile bind invocation: installs `gomobile`/`gobind` into an isolated scratch module (never touches `vitl`'s `go.mod`), sets `JAVA_HOME` for Homebrew openjdk@17, uses `-androidapi 21` matching the NDK 28.2.13676358 range, writes `android/app/libs/vitallink.aar`. Run to regenerate AAR whenever `mobile/vitallink/vitallink.go` changes. |
| — | `android/` | Done. Minimal Gradle project skeleton: `settings.gradle.kts`, root/app `build.gradle.kts` (minSdk 21, depends on `libs/vitallink.aar`), `AndroidManifest.xml` (INTERNET + CAMERA + READ_MEDIA_IMAGES permissions, FileProvider for camera intent), `MainActivity.kt` (implements `vitallink.Listener`, dispatches Go callbacks via `runOnUiThread`, camera capture → downscale → `sendImage`), `activity_main.xml` (plain LinearLayout/ScrollView with all session/vitals/image/end-session controls). **On-device verification outstanding** — `assembleDebug` not yet run; no emulator/device was attached during this session. See note below. |

**Verified by running it** (not just `go build`): full session handshake, 14/14 vitals ACKed and relayed to a WS client, one clean audio transfer (opusenc → chunk → sliding-window send → server reassembly → single WS broadcast, no duplicates), one clean image transfer (JPEG downscale → 22-chunk sliding-window send → server reassembly → single WS broadcast, rendered correctly in the dashboard), patient-not-found rejection path, baseline HTTP client success path. Also verified live in a real browser (headless Chromium via the `browse` skill): loaded `dashboard/index.html`, watched it connect to `ws://localhost:8080/ws`, ran a field-client session against it, confirmed the EHR panel, live vitals chart, "SpO2 Low" threshold highlighting, and link diagnostics all populate correctly, confirmed the "Signal Doctor Ready" button round-trips through the server as a `DOCTOR_READY` UDP packet the field client receives, and confirmed a field-sent image renders in the "Field Media Stream" panel.

## Bugs found & fixed during implementation

1. **Temp field byte overflow.** Spec says VITALS `temp` is 1 byte, "x10 for one decimal" (e.g. 368 for 36.8°C) — that doesn't fit in a byte and silently truncated (36.8°C displayed as 11.1°C). Fixed with an offset encoding (`protocol.EncodeTempByte`/`DecodeTempByte`, offset 300, covering 30.0–55.5°C in one byte).
2. **Duplicate media broadcast.** Original design had both the chunk handler and a periodic watcher goroutine independently able to detect "transfer complete" and broadcast to the dashboard, racing under load. Refactored to a single event-driven watcher goroutine per transfer (mediaID-keyed, kicked on completion, ticked otherwise) that is the sole owner of NACK-sending and completion broadcast.
3. **Concurrent-read hazard in field-client.** Initial ARQ design had each sender (session, vitals, media) call `conn.Read` directly — unsafe with multiple concurrent senders on one UDP socket. Reworked into one receive loop that fans packets out to per-type/per-transfer channels; `transport.SendWithRetry` and `MediaSender` now consume from a channel instead of reading the socket themselves.

## Not yet done / stretch scope

- **Phase 9.5 (polish, optional per plan):** delta-encoding flag logic (`delta_flag` is wired into the packet but always sent as `0`/full snapshot — no actual diffing logic). The dashboard's `0x01`–`0x04` quick-instruction buttons are wired in the UI but not yet confirmed to send `DOCTOR_MSG` over the socket end-to-end (only "Signal Doctor Ready" was verified live).
- **Phase 10 (demo prep):** not applicable to code implementation — backup video, pitch script.
- No automated test suite yet (`go test ./...` has no tests); verification so far has been live end-to-end runs, not unit tests.
- **Android on-device verification outstanding:** `android/` Gradle skeleton and `mobile/vitallink` Go package are both written and `go build`/`go vet` pass. The next steps are: (1) run `mobile/build_android.sh` to produce `vitallink.aar`; (2) `cd android && ./gradlew assembleDebug` to confirm APK compiles; (3) install on an emulator/device and manually exercise Start Session → Send Vitals → Capture Image against a running `cmd/server`. Audio is deliberately excluded from the mobile app (see `docs/mobile.md` scope decisions: `opusenc` is a desktop CLI and cannot shell out on Android).
