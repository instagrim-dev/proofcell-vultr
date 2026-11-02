package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatusDescribesScaffoldHonestly(t *testing.T) {
	s := New(nil)
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if r.Code != http.StatusOK {
		t.Fatalf("status %d", r.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["mode"] != "local-fixture" || body["vultr_deployed"] != false {
		t.Fatalf("unexpected status %v", body)
	}
}
func TestRunRequiresConfiguredRunner(t *testing.T) {
	s := New(nil)
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/api/runs/boundary", nil))
	if r.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", r.Code)
	}
}
