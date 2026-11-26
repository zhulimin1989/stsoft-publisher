package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// ReadinessCheck is a named dependency probe used by /readyz. The fn should
// return nil when the dependency is reachable and usable. Probes share a
// 2s budget enforced by HandleReadyz.
type ReadinessCheck struct {
	Name string
	Fn   func(context.Context) error
}

// AddReadinessCheck registers a dependency probe with /readyz. Order is
// preserved in the response. Call from main after wiring the dependency.
func (s *Server) AddReadinessCheck(name string, fn func(context.Context) error) {
	s.readyChecks = append(s.readyChecks, ReadinessCheck{Name: name, Fn: fn})
}

// HandleLivez reports process liveness. Always 200 as long as the process
// can serve HTTP — k8s liveness should only restart on hard crashes, not
// transient dep failures.
func (s *Server) HandleLivez(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// HandleHealthz is the legacy alias for /livez (Google "z-page" convention,
// kept for callers wired before /livez existed).
func (s *Server) HandleHealthz(w http.ResponseWriter, r *http.Request) {
	s.HandleLivez(w, r)
}

// HandleReadyz reports whether all registered dependencies are reachable.
// Returns 200 when every probe passes, 503 otherwise. Body always lists
// per-dep status so operators can see which one failed.
func (s *Server) HandleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	checks := make(map[string]string, len(s.readyChecks))
	allOK := true
	for _, c := range s.readyChecks {
		if err := c.Fn(ctx); err != nil {
			checks[c.Name] = "fail: " + err.Error()
			allOK = false
			continue
		}
		checks[c.Name] = "ok"
	}
	status := http.StatusOK
	body := "ok"
	if !allOK {
		status = http.StatusServiceUnavailable
		body = "not ready"
	}
	writeJSON(w, status, map[string]any{"status": body, "checks": checks})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
