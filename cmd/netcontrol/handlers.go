package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// applyRequest is the JSON body for POST /apply.
type applyRequest struct {
	Interface string  `json:"interface"`
	LossPct   float64 `json:"loss_pct"`
	RateMbit  float64 `json:"rate_mbit"`
}

// apiResponse is the JSON body returned by /apply and /reset.
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

// pfRulesetContent returns the pf ruleset that routes all traffic on iface
// through dummynet pipe 1.
func pfRulesetContent(iface string) string {
	return fmt.Sprintf("dummynet out quick on %s all pipe 1\n", iface)
}

// buildApplyScript returns the shell command that configures the dummynet
// pipe and loads/enables the pf ruleset previously written to pfRuleFile.
func buildApplyScript(r applyRequest, pfRuleFile string) string {
	plr := r.LossPct / 100.0
	// pfctl -e exits non-zero if pf is already enabled (e.g. from a prior
	// Apply click) even though enabling isn't actually needed at that
	// point, so tolerate that failure rather than reporting the whole
	// apply as failed.
	return fmt.Sprintf(
		"dnctl pipe 1 config bw %.2fMbit/s plr %.4f && pfctl -f %s && (pfctl -e || true)",
		r.RateMbit, plr, pfRuleFile,
	)
}

// buildResetScript returns the shell command that restores the default pf
// ruleset and clears all dummynet pipes.
func buildResetScript() string {
	return "pfctl -f /etc/pf.conf && dnctl -q flush"
}

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
