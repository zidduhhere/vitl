# macOS Network Conditions Control Panel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a standalone local web tool (`cmd/netcontrol` + `network-sim/macos-control/`) that lets a developer set packet loss % and bandwidth cap (Mbit/s) via sliders, and applies them to a real macOS network interface using `dnctl`/`pf` (dummynet), the macOS equivalent of Linux's `tc`/`netem`.

**Architecture:** A tiny Go HTTP server (`cmd/netcontrol/main.go`) serves a static slider page and exposes `POST /apply` and `POST /reset`. Apply shells out to `dnctl pipe config` + `pfctl -f` wrapped in a single `osascript ... with administrator privileges` call (one native macOS password dialog per Apply click). Reset restores `/etc/pf.conf` and flushes dummynet pipes the same way.

**Tech Stack:** Go (stdlib `net/http`, `os/exec`), vanilla HTML/CSS/JS (no frameworks, matching `dashboard/`), macOS `dnctl`/`pfctl`/`osascript`.

## Global Constraints

- Loss + speed only — no delay/latency control (spec explicitly excludes it).
- Backend listens on localhost only, default port `:8090`.
- Apply only fires on button click, never on slider drag (avoids repeated admin-privileges prompts).
- Reset must be idempotent (safe to click with nothing applied).
- Tool is local dev/testing only — it replaces the active pf ruleset while applied; this is stated on the page, not hidden.
- No automated tests against the real network stack — verify manually per spec (`dnctl -q pipe show`, `pfctl -sr`, `ping`/`curl` before/after).
- Go module is `github.com/zidduhhere/vitl`, Go 1.26.3. Follow `cmd/server/main.go`'s `flag.String` pattern for configurable listen address.

---

### Task 1: Static control panel page (HTML/CSS/JS)

**Files:**
- Create: `network-sim/macos-control/index.html`
- Create: `network-sim/macos-control/style.css`
- Create: `network-sim/macos-control/app.js`

**Interfaces:**
- Produces: a page that POSTs JSON `{ "interface": string, "loss_pct": number, "rate_mbit": number }` to `/apply` and an empty POST to `/reset`, both same-origin (served by the Task 2 backend on the same port), and renders `{ "ok": bool, "error"?: string }` responses.

- [ ] **Step 1: Write `network-sim/macos-control/index.html`**

```html
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>VitalLink Network Conditions (macOS)</title>
  <link rel="stylesheet" href="style.css" />
</head>
<body>
  <main>
    <h1>Network Conditions Control</h1>
    <p class="notice">
      For local dev/testing only. Applying replaces your active pf ruleset
      on the chosen interface until you click Reset.
    </p>

    <div class="field">
      <label for="iface">Interface</label>
      <input type="text" id="iface" value="en0" />
    </div>

    <div class="field">
      <label for="lossRange">Packet Loss (%)</label>
      <div class="slider-row">
        <input type="range" id="lossRange" min="0" max="100" step="1" value="0" />
        <input type="number" id="lossNumber" min="0" max="100" step="1" value="0" />
      </div>
    </div>

    <div class="field">
      <label for="rateRange">Speed Cap (Mbit/s)</label>
      <div class="slider-row">
        <input type="range" id="rateRange" min="0.1" max="100" step="0.1" value="10" />
        <input type="number" id="rateNumber" min="0.1" max="100" step="0.1" value="10" />
      </div>
    </div>

    <div class="actions">
      <button id="applyBtn">Apply</button>
      <button id="resetBtn">Reset</button>
    </div>

    <div id="status" class="status"></div>
  </main>
  <script src="app.js"></script>
</body>
</html>
```

- [ ] **Step 2: Write `network-sim/macos-control/style.css`**

```css
body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  max-width: 480px;
  margin: 40px auto;
  color: #1a1a1a;
  background: #fafafa;
}

h1 {
  font-size: 1.4rem;
  margin-bottom: 0.5rem;
}

.notice {
  font-size: 0.85rem;
  color: #7a4a00;
  background: #fff3cd;
  border: 1px solid #ffe08a;
  border-radius: 6px;
  padding: 8px 12px;
  margin-bottom: 1.5rem;
}

.field {
  margin-bottom: 1.25rem;
}

.field label {
  display: block;
  font-weight: 600;
  margin-bottom: 4px;
  font-size: 0.9rem;
}

.slider-row {
  display: flex;
  align-items: center;
  gap: 12px;
}

.slider-row input[type="range"] {
  flex: 1;
}

.slider-row input[type="number"] {
  width: 70px;
}

#iface {
  width: 100%;
  padding: 6px 8px;
  box-sizing: border-box;
}

.actions {
  display: flex;
  gap: 10px;
  margin-top: 1.5rem;
}

button {
  padding: 8px 18px;
  font-size: 0.95rem;
  border-radius: 6px;
  border: 1px solid #ccc;
  cursor: pointer;
  background: white;
}

#applyBtn {
  background: #1a73e8;
  color: white;
  border-color: #1a73e8;
}

#resetBtn {
  background: white;
}

.status {
  margin-top: 1rem;
  font-size: 0.9rem;
  min-height: 1.2rem;
}

.status.error {
  color: #b3261e;
}

.status.ok {
  color: #1e7d32;
}
```

- [ ] **Step 3: Write `network-sim/macos-control/app.js`**

```javascript
// VitalLink macOS Network Conditions Control Panel

const lossRange = document.getElementById("lossRange");
const lossNumber = document.getElementById("lossNumber");
const rateRange = document.getElementById("rateRange");
const rateNumber = document.getElementById("rateNumber");
const ifaceInput = document.getElementById("iface");
const applyBtn = document.getElementById("applyBtn");
const resetBtn = document.getElementById("resetBtn");
const statusEl = document.getElementById("status");

function syncPair(rangeEl, numberEl) {
  rangeEl.addEventListener("input", () => {
    numberEl.value = rangeEl.value;
  });
  numberEl.addEventListener("input", () => {
    rangeEl.value = numberEl.value;
  });
}

syncPair(lossRange, lossNumber);
syncPair(rateRange, rateNumber);

function setStatus(message, kind) {
  statusEl.textContent = message;
  statusEl.className = "status" + (kind ? " " + kind : "");
}

async function postJSON(path, body) {
  const res = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json();
  if (!res.ok || !data.ok) {
    throw new Error(data.error || `request to ${path} failed`);
  }
  return data;
}

applyBtn.addEventListener("click", async () => {
  applyBtn.disabled = true;
  setStatus("Applying…", "");
  try {
    await postJSON("/apply", {
      interface: ifaceInput.value.trim(),
      loss_pct: Number(lossNumber.value),
      rate_mbit: Number(rateNumber.value),
    });
    setStatus(
      `Applied: ${lossNumber.value}% loss, ${rateNumber.value} Mbit/s on ${ifaceInput.value.trim()}`,
      "ok"
    );
  } catch (err) {
    setStatus(`Error: ${err.message}`, "error");
  } finally {
    applyBtn.disabled = false;
  }
});

resetBtn.addEventListener("click", async () => {
  resetBtn.disabled = true;
  setStatus("Resetting…", "");
  try {
    await postJSON("/reset");
    setStatus("Reset complete.", "ok");
  } catch (err) {
    setStatus(`Error: ${err.message}`, "error");
  } finally {
    resetBtn.disabled = false;
  }
});
```

- [ ] **Step 4: Manually verify the page renders**

Run: `python3 -m http.server 8091 --directory network-sim/macos-control`
Open `http://localhost:8091` in a browser.
Expected: page loads, sliders and number inputs move together, Apply/Reset buttons are visible (clicking them will fail with a fetch/network error since there's no backend yet — that's expected at this step). Stop the server with Ctrl-C.

- [ ] **Step 5: Commit**

```bash
git add network-sim/macos-control/index.html network-sim/macos-control/style.css network-sim/macos-control/app.js
git commit -m "feat: add macOS network conditions control panel UI"
```

---

### Task 2: netcontrol Go backend — request validation

**Files:**
- Create: `cmd/netcontrol/main.go`
- Create: `cmd/netcontrol/handlers.go`
- Test: `cmd/netcontrol/handlers_test.go`

**Interfaces:**
- Consumes: nothing from Task 1 at compile time (frontend and backend are decoupled via HTTP JSON contract defined in the spec).
- Produces:
  - `type applyRequest struct { Interface string; LossPct float64; RateMbit float64 }` (JSON tags: `interface`, `loss_pct`, `rate_mbit`)
  - `func validateApplyRequest(r applyRequest) error` — returns nil if `Interface` non-empty, `0 <= LossPct <= 100`, `RateMbit > 0`; otherwise a descriptive error.
  - These are consumed by Task 3's HTTP handlers.

- [ ] **Step 1: Write the failing test**

Create `cmd/netcontrol/handlers_test.go`:

```go
package main

import "testing"

func TestValidateApplyRequest(t *testing.T) {
	cases := []struct {
		name    string
		req     applyRequest
		wantErr bool
	}{
		{"valid", applyRequest{Interface: "en0", LossPct: 20, RateMbit: 5}, false},
		{"empty interface", applyRequest{Interface: "", LossPct: 20, RateMbit: 5}, true},
		{"negative loss", applyRequest{Interface: "en0", LossPct: -1, RateMbit: 5}, true},
		{"loss over 100", applyRequest{Interface: "en0", LossPct: 101, RateMbit: 5}, true},
		{"zero rate", applyRequest{Interface: "en0", LossPct: 20, RateMbit: 0}, true},
		{"negative rate", applyRequest{Interface: "en0", LossPct: 20, RateMbit: -5}, true},
		{"zero loss is valid", applyRequest{Interface: "en0", LossPct: 0, RateMbit: 5}, false},
		{"loss of 100 is valid", applyRequest{Interface: "en0", LossPct: 100, RateMbit: 5}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateApplyRequest(tc.req)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/netcontrol/... -run TestValidateApplyRequest -v`
Expected: FAIL — `applyRequest` and `validateApplyRequest` are undefined (compile error).

- [ ] **Step 3: Write minimal implementation**

Create `cmd/netcontrol/handlers.go`:

```go
package main

import "fmt"

// applyRequest is the JSON body for POST /apply.
type applyRequest struct {
	Interface string  `json:"interface"`
	LossPct   float64 `json:"loss_pct"`
	RateMbit  float64 `json:"rate_mbit"`
}

// applyResponse and errors are shared by /apply and /reset.
type apiResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func validateApplyRequest(r applyRequest) error {
	if r.Interface == "" {
		return fmt.Errorf("interface must not be empty")
	}
	if r.LossPct < 0 || r.LossPct > 100 {
		return fmt.Errorf("loss_pct must be between 0 and 100, got %v", r.LossPct)
	}
	if r.RateMbit <= 0 {
		return fmt.Errorf("rate_mbit must be greater than 0, got %v", r.RateMbit)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/netcontrol/... -run TestValidateApplyRequest -v`
Expected: PASS (all 8 subtests)

- [ ] **Step 5: Commit**

```bash
git add cmd/netcontrol/handlers.go cmd/netcontrol/handlers_test.go
git commit -m "feat: add netcontrol request validation"
```

---

### Task 3: netcontrol Go backend — dnctl/pf command builders

**Files:**
- Modify: `cmd/netcontrol/handlers.go`
- Test: `cmd/netcontrol/handlers_test.go`

**Interfaces:**
- Consumes: `applyRequest` from Task 2.
- Produces:
  - `func buildApplyScript(r applyRequest, pfRuleFile string) string` — returns the full shell command string (dnctl config + pfctl load/enable) to be passed to `osascript ... with administrator privileges`.
  - `func buildResetScript() string` — returns the shell command string for reset.
  - `func pfRulesetContent(iface string) string` — returns the pf ruleset file contents for a given interface.
  - These are consumed by Task 4's HTTP handlers (which write `pfRulesetContent` to a temp file, then exec `buildApplyScript`/`buildResetScript` via osascript).

- [ ] **Step 1: Write the failing test**

Add to `cmd/netcontrol/handlers_test.go`:

```go
func TestPfRulesetContent(t *testing.T) {
	got := pfRulesetContent("en0")
	want := "dummynet out quick on en0 all pipe 1\n"
	if got != want {
		t.Fatalf("pfRulesetContent(%q) = %q, want %q", "en0", got, want)
	}
}

func TestBuildApplyScript(t *testing.T) {
	req := applyRequest{Interface: "en0", LossPct: 20, RateMbit: 5}
	got := buildApplyScript(req, "/tmp/vitl-netsim.pf")

	wantSubstrings := []string{
		"dnctl pipe 1 config bw 5.00Mbit/s plr 0.2000",
		"pfctl -f /tmp/vitl-netsim.pf",
		"pfctl -e",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(got, want) {
			t.Errorf("buildApplyScript output missing %q; got: %s", want, got)
		}
	}
}

func TestBuildResetScript(t *testing.T) {
	got := buildResetScript()
	wantSubstrings := []string{
		"pfctl -f /etc/pf.conf",
		"dnctl -q flush",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(got, want) {
			t.Errorf("buildResetScript output missing %q; got: %s", want, got)
		}
	}
}
```

Add `"strings"` to the test file's imports (`import ("strings"; "testing")`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/netcontrol/... -v`
Expected: FAIL — `pfRulesetContent`, `buildApplyScript`, `buildResetScript` undefined (compile error).

- [ ] **Step 3: Write minimal implementation**

Append to `cmd/netcontrol/handlers.go`:

```go
// pfRulesetContent returns the pf ruleset that routes all traffic on iface
// through dummynet pipe 1.
func pfRulesetContent(iface string) string {
	return fmt.Sprintf("dummynet out quick on %s all pipe 1\n", iface)
}

// buildApplyScript returns the shell command that configures the dummynet
// pipe and loads/enables the pf ruleset previously written to pfRuleFile.
func buildApplyScript(r applyRequest, pfRuleFile string) string {
	plr := r.LossPct / 100.0
	return fmt.Sprintf(
		"dnctl pipe 1 config bw %.2fMbit/s plr %.4f && pfctl -f %s && pfctl -e",
		r.RateMbit, plr, pfRuleFile,
	)
}

// buildResetScript returns the shell command that restores the default pf
// ruleset and clears all dummynet pipes.
func buildResetScript() string {
	return "pfctl -f /etc/pf.conf && dnctl -q flush"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/netcontrol/... -v`
Expected: PASS (all tests, including Task 2's)

- [ ] **Step 5: Commit**

```bash
git add cmd/netcontrol/handlers.go cmd/netcontrol/handlers_test.go
git commit -m "feat: add netcontrol dnctl/pf command builders"
```

---

### Task 4: netcontrol Go backend — HTTP handlers and server entrypoint

**Files:**
- Modify: `cmd/netcontrol/handlers.go`
- Create: `cmd/netcontrol/main.go`
- Test: `cmd/netcontrol/handlers_test.go`

**Interfaces:**
- Consumes: `applyRequest`, `apiResponse`, `validateApplyRequest`, `buildApplyScript`, `buildResetScript`, `pfRulesetContent` from Tasks 2–3.
- Produces: `func newMux() *http.ServeMux` registering `POST /apply`, `POST /reset`, and static file serving from `network-sim/macos-control/` at `/`. Used directly by `main()`.

- [ ] **Step 1: Write the failing test**

Add to `cmd/netcontrol/handlers_test.go`:

```go
func TestApplyHandlerRejectsInvalidRequest(t *testing.T) {
	body := `{"interface":"","loss_pct":20,"rate_mbit":5}`
	req := httptest.NewRequest(http.MethodPost, "/apply", strings.NewReader(body))
	rec := httptest.NewRecorder()

	applyHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var resp apiResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.OK {
		t.Fatalf("expected ok=false for invalid request")
	}
	if resp.Error == "" {
		t.Fatalf("expected non-empty error message")
	}
}

func TestApplyHandlerRejectsWrongMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/apply", nil)
	rec := httptest.NewRecorder()

	applyHandler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
```

Add `"encoding/json"`, `"net/http"`, `"net/http/httptest"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/netcontrol/... -v`
Expected: FAIL — `applyHandler` undefined (compile error).

- [ ] **Step 3: Write minimal implementation**

Append to `cmd/netcontrol/handlers.go` (add `"encoding/json"`, `"net/http"`, `"os"`, `"os/exec"` to imports):

```go
func writeJSON(w http.ResponseWriter, status int, resp apiResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

// runPrivileged executes script as a single shell command with macOS's
// native administrator-privileges prompt, once per call.
func runPrivileged(script string) error {
	escaped := strings.ReplaceAll(script, `"`, `\"`)
	osaScript := fmt.Sprintf(`do shell script "%s" with administrator privileges`, escaped)
	cmd := exec.Command("osascript", "-e", osaScript)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func applyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{OK: false, Error: "method not allowed"})
		return
	}

	var req applyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{OK: false, Error: "invalid JSON body"})
		return
	}
	if err := validateApplyRequest(req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{OK: false, Error: err.Error()})
		return
	}

	pfRuleFile := "/tmp/vitl-netsim.pf"
	if err := os.WriteFile(pfRuleFile, []byte(pfRulesetContent(req.Interface)), 0644); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{OK: false, Error: err.Error()})
		return
	}

	if err := runPrivileged(buildApplyScript(req, pfRuleFile)); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}

func resetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{OK: false, Error: "method not allowed"})
		return
	}

	if err := runPrivileged(buildResetScript()); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, apiResponse{OK: true})
}

func newMux(staticDir string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/apply", applyHandler)
	mux.HandleFunc("/reset", resetHandler)
	mux.Handle("/", http.FileServer(http.Dir(staticDir)))
	return mux
}
```

Create `cmd/netcontrol/main.go`:

```go
// Command netcontrol serves a local slider UI for setting packet loss and
// bandwidth caps on a macOS network interface via dnctl/pf (dummynet).
// It requires macOS (dnctl/pfctl/osascript) and is for local dev/testing
// only.
package main

import (
	"flag"
	"log"
	"net/http"
)

func main() {
	addr := flag.String("addr", ":8090", "listen address for the netcontrol UI/API")
	staticDir := flag.String("static", "network-sim/macos-control", "directory containing the control panel static files")
	flag.Parse()

	mux := newMux(*staticDir)
	log.Printf("netcontrol listening on %s (serving %s)", *addr, *staticDir)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/netcontrol/... -v`
Expected: PASS (all tests from Tasks 2–4)

Run: `go build ./cmd/netcontrol/`
Expected: builds with no errors.

- [ ] **Step 5: Commit**

```bash
git add cmd/netcontrol/handlers.go cmd/netcontrol/main.go cmd/netcontrol/handlers_test.go
git commit -m "feat: add netcontrol HTTP handlers and server entrypoint"
```

---

### Task 5: End-to-end manual verification on macOS

**Files:** none (manual verification only, per spec's out-of-scope note that no automated tests exercise the real network stack)

**Interfaces:**
- Consumes: the full `cmd/netcontrol` binary and `network-sim/macos-control/` page from Tasks 1–4.
- Produces: nothing (verification task).

- [ ] **Step 1: Run the server**

Run: `go run ./cmd/netcontrol/`
Expected output: `netcontrol listening on :8090 (serving network-sim/macos-control)`

- [ ] **Step 2: Open the UI and inspect defaults**

Open `http://localhost:8090` in a browser.
Expected: page loads with Interface defaulting to `en0`, Loss 0%, Speed 10 Mbit/s, moving sliders updates the paired number field.

- [ ] **Step 3: Apply a throttle and verify via dnctl/pfctl**

In the UI, set Loss to `20`, Speed to `1`, Interface to your active interface (confirm with `ifconfig | grep -B1 "status: active"` in another terminal — commonly `en0`). Click Apply.
Expected: a native macOS administrator-privileges password dialog appears once; after entering the password, the status banner shows "Applied: 20% loss, 1 Mbit/s on en0".

Run in a terminal: `sudo dnctl -q pipe show`
Expected: shows pipe 1 configured with `1.000 Mbit/s`, `20.00%` packet loss (or equivalent `plr 0.2000`).

Run: `sudo pfctl -sr`
Expected: shows the rule `dummynet out quick on en0 all pipe 1`.

- [ ] **Step 4: Verify the throttle affects real traffic**

Run: `ping -c 5 -i 0.2 8.8.8.8` (or any reachable host) while the throttle is applied.
Expected: noticeably increased packet loss / degraded timing compared to an unthrottled baseline (run the same ping before Apply and after Reset to compare).

- [ ] **Step 5: Reset and verify restoration**

In the UI, click Reset.
Expected: another administrator-privileges dialog appears once; status banner shows "Reset complete."

Run: `sudo pfctl -sr`
Expected: the `dummynet ... pipe 1` rule from Step 3 is gone (macOS's default ruleset is restored).

Run: `sudo dnctl -q pipe show`
Expected: no pipes configured (or pipe 1 no longer present/active).

- [ ] **Step 6: Record verification result**

No commit needed for this task — it's a manual check. If any step fails, return to the relevant earlier task, fix, and re-run this task's steps from Step 1.
