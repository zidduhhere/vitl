# VitalLink — Android Field-Worker App (gomobile)

## Context

The desktop `cmd/field-client` binary simulates a field health worker's device: it opens a UDP session with the server, streams vitals, and can send chunked audio/image over the sliding-window media transport. The user wants a real Android app for field health workers specifically — built with `gomobile`, the standard way to expose Go code as a native Android library (`.aar`) that a thin Kotlin UI calls into.

`internal/protocol` and `internal/transport` (packet encode/decode, ARQ, sliding-window sender) are already reusable Go code shared by every binary in this repo. The mobile app reuses them directly rather than reimplementing networking in Kotlin — the same reason `cmd/field-client` and `cmd/server` share them.

**Toolchain already verified working in this environment** (hands-on, not assumed):

- Android SDK at `~/Library/Android/sdk`, NDK `28.2.13676358` present.
- `gomobile`/`gobind` installable via `go install`, but must be installed from an **isolated scratch module** (not `vitl`'s own `go.mod`) — installing them directly in the project pulls `golang.org/x/mobile` (and its `x/tools`, `x/mod`, `x/sync` deps) into the main module's dependency graph as a false "runtime" dependency, when it's actually a build-time-only tool.
- `gomobile bind` requires `-androidapi 21` explicitly — this NDK's `platforms.json` reports `min: 21, max: 35`, but gomobile's own default (`16`) is outside that range and the bind fails without the flag.
- `javac` requires a real JDK on `JAVA_HOME`/PATH — Homebrew's `openjdk@17` is installed but not linked; must export `JAVA_HOME="$(brew --prefix openjdk@17)"` (or symlink) before `gomobile bind` will succeed on this machine.
- A trivial `gomobile bind -target=android -androidapi 21 -o /tmp/hello.aar` against a one-function scratch package got past the SDK/NDK detection step and reached the `javac` step before failing on the missing JDK — so once `JAVA_HOME` is fixed, the toolchain is confirmed functional end-to-end.

**Scope decisions already made with the user:**

- Audio capture is **out of scope** for the mobile app. `opusenc` is a desktop CLI shelled out via `os/exec` in `internal/media/audio.go` — there's no shell/binary to shell out to on Android, and bundling per-ABI opus-tools binaries is disproportionate build cost for this app. Mobile ships vitals streaming + image capture/send only.
- UI is a **single minimal Activity**, plain Android views (no Material polish, no multi-screen navigation) — a field-worker utility, not a demo showpiece.

## Approach

### 1. `mobile/vitallink` — the bindable Go package

New package `mobile/vitallink/vitallink.go` in the `vitl` module (importable internal packages work fine since it's under the same module root). Exposes a `gomobile bind`-safe API — only types bind supports: `string`, `bool`, signed integers, `[]byte`, exported structs/interfaces. No `uint32`, no channels, no generics in the exported surface.

- `type Client struct { ... }` (opaque; holds the UDP conn, session token, seq counter, mutex, receive-loop state)
- `func NewClient(serverAddr string, listener Listener) (*Client, error)` — dials UDP, starts an internal receive-loop goroutine that dispatches incoming packets and invokes `listener` callbacks (gomobile bind supports Java implementing a Go-defined interface and passing it in — this is the standard callback pattern for async events crossing the JNI boundary).
- `type Listener interface { OnSessionStatus(status string, sessionToken int64); OnDoctorReady(doctorID int, message string); OnDoctorMsg(code int); OnVitalsAck(seq int, ok bool); OnMediaProgress(mediaID int, sent int, total int) }`
- `func (c *Client) StartSession(workerID, patientID int64) error` — builds `protocol.SessionInitPacket` (casting `int64`→`uint32`), sends via `transport.SendWithRetry`, reports result through `listener.OnSessionStatus`.
- `func (c *Client) SendVitals(heartRate, spo2, bpSystolic, bpDiastolic, tempX10 int) error` — builds `protocol.VitalsPacket` (reusing `protocol.EncodeTempByte`), short-timeout/few-retry ARQ per the freshness-over-completeness principle already established in `cmd/field-client/main.go`, reports via `listener.OnVitalsAck`.
- `func (c *Client) SendImage(jpegBytes []byte) error` — chunks via the existing `transport.MediaSender` (same one `cmd/field-client` uses), reports progress via `listener.OnMediaProgress`. Takes already-encoded JPEG bytes so the Go layer doesn't need to touch Android's camera/bitmap APIs — the Kotlin side captures via `MediaStore`/camera intent, downscales with Android's own `Bitmap.compress(JPEG, ...)`, and hands raw bytes across the JNI boundary. (Simpler and more idiomatic than routing through `internal/media/image.go`'s stdlib-`image` decode path, which has no reason to run on-device when Android's own bitmap APIs already do it.)
- `func (c *Client) EndSession() error`
- `func (c *Client) Close()`

Internally reuses `internal/protocol` and `internal/transport` — no protocol logic duplicated. Mirrors the channel-based receive-loop pattern already proven in `cmd/field-client/main.go` (avoids the concurrent-UDP-read bug that was already found and fixed there).

**Verification for this step:** `go build ./mobile/...` and `go vet ./mobile/...` from the main module — confirms the package compiles standalone before attempting a bind, same discipline used for every other package in this repo so far.

### 2. Build the AAR

`mobile/build_android.sh` — scripts the exact sequence validated above:

```bash
# Installs gomobile/gobind into an isolated scratch module under $TMPDIR
# (never touches vitl's go.mod), then runs:
JAVA_HOME="$(brew --prefix openjdk@17)" \
ANDROID_HOME="$HOME/Library/Android/sdk" \
ANDROID_NDK_HOME="$ANDROID_HOME/ndk/28.2.13676358" \
gomobile bind -target=android -androidapi 21 \
  -o android/app/libs/vitallink.aar \
  ./mobile/vitallink
```

This is the one artifact that has to be regenerated whenever `mobile/vitallink` changes — documented in the script's header comment.

**Verification:** actually run this script and confirm `vitallink.aar` is produced (the environment is proven capable of reaching this point — the earlier scratch test got past SDK/NDK detection into the `javac` step).

### 3. Android Studio project skeleton

`android/` — minimal Gradle project:

- `android/settings.gradle.kts`, `android/build.gradle.kts` (root)
- `android/app/build.gradle.kts` — depends on `libs/vitallink.aar` (`implementation(files("libs/vitallink.aar"))`), `minSdk 21` (matches the `-androidapi 21` bind target), single `app` module.
- `android/app/src/main/AndroidManifest.xml` — `INTERNET` permission (UDP/network), `CAMERA` + `READ_MEDIA_IMAGES`/`READ_EXTERNAL_STORAGE` as needed for image capture.
- `android/app/src/main/java/link/vitl/field/MainActivity.kt` — single Activity implementing `vitallink.Listener`:
  - Text fields: server address, worker ID, patient ID.
  - "Start Session" button → `Client.startSession(...)`, disables itself and enables the session controls on `onSessionStatus`.
  - Live status text + last-vitals-ack indicator, driven by the `Listener` callbacks (must dispatch UI updates via `runOnUiThread` since they arrive from the Go receive-loop goroutine, not the main thread).
  - "Send Vitals" button — manual numeric entry fields (heart rate, SpO2, BP, temp) mirroring what a field worker would read off a monitor, since there's no real sensor hardware to read from in this app; calls `client.sendVitals(...)`.
  - "Capture & Send Image" button — launches `MediaStore.ACTION_IMAGE_CAPTURE` (or a gallery picker as fallback), downscales the resulting `Bitmap` (long side ~320px to match the desktop client's budget), JPEG-compresses at quality ~40, passes bytes to `client.sendImage(...)`. Progress shown via `onMediaProgress`.
  - "End Session" button → `client.endSession()`.
- `android/app/src/main/res/layout/activity_main.xml` — plain `LinearLayout`/`ScrollView` with the above controls, no custom styling.

**Verification:** this repo's environment has no Android emulator/device attached and no way to run `./gradlew assembleDebug` verified in this session yet — that command needs to actually be run and an APK produced/installed as the real verification step (emulator or physical device). This is called out explicitly as the one part of this plan that can't be end-to-end verified purely from this shell the way the rest of the backend was (live UDP/WS runs, browser screenshots). If a connected device or running emulator exists, `./gradlew installDebug` should be attempted at build time; otherwise the deliverable is verified via `assembleDebug` (compiles) plus a manual note in `docs/status.md` that on-device testing is outstanding.

### 4. Docs

Update `docs/status.md` with a new row for the mobile app, matching the existing table format — what's done, what's verified vs. not (per the emulator/device caveat above), and the audio-scope decision recorded as a deliberate cut (not an oversight), same as how the temp-byte and duplicate-broadcast bugs were documented.

## Files touched

- New: `mobile/vitallink/vitallink.go`
- New: `mobile/build_android.sh`
- New: `android/` (Gradle project skeleton — settings/build files, manifest, `MainActivity.kt`, `activity_main.xml`)
- Modified: `docs/status.md`

## Verification plan

1. `go build ./mobile/... && go vet ./mobile/...` from the main module.
2. Run `mobile/build_android.sh`, confirm `vitallink.aar` is produced.
3. `cd android && ./gradlew assembleDebug`, confirm APK builds.
4. If an emulator/device is available: install and manually exercise Start Session → Send Vitals → Capture Image against a running `cmd/server`, confirming the same dashboard flow already verified for the desktop client (EHR panel populates, vitals chart updates, image renders) also works from the Android app. If no device/emulator is available, state that explicitly as an open item rather than claiming untested behavior works.
