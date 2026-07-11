package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
