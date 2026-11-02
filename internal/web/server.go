package web

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/instagrim-dev/proofcell-vultr/internal/sandbox"
)

//go:embed index.html
var index string

type Runner interface {
	Run(context.Context) (sandbox.Report, error)
}
type server struct {
	runner Runner
	active chan struct{}
}

func New(r Runner) http.Handler {
	s := &server{runner: r, active: make(chan struct{}, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(index))
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"mode": "local-fixture", "vultr_deployed": false, "model_connected": false, "runner_available": s.runner != nil})
	})
	mux.HandleFunc("POST /api/runs/boundary", s.run)
	return mux
}
func (s *server) run(w http.ResponseWriter, r *http.Request) {
	if s.runner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Runner is not configured."})
		return
	}
	select {
	case s.active <- struct{}{}:
		defer func() { <-s.active }()
	default:
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "A fixture is already running."})
		return
	}
	report, err := s.runner.Run(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error(), "report": report})
		return
	}
	writeJSON(w, http.StatusOK, report)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
