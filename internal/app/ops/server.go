package ops

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Server exposes the agent-facing HTTP surface. It is mounted under /ops/ by
// nginx and only ever answers the agent -- the bot talks to Service in-process.
type Server struct {
	svc *Service
	// BinaryPath is the built daskgram-ops binary served to users who install the
	// agent. Empty disables the download route.
	BinaryPath string
}

func NewServer(svc *Service) *Server { return &Server{svc: svc} }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/enroll", s.handleEnroll)
	mux.HandleFunc("POST /api/poll", s.handlePoll)
	mux.HandleFunc("POST /api/result", s.handleResult)
	mux.HandleFunc("GET /download/daskgram-ops", s.handleDownload)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	return mux
}

// handleDownload serves the agent binary. It is public on purpose: the binary is
// not a secret, and the enrollment code is what actually authorises an agent.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if s.BinaryPath == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="daskgram-ops"`)
	http.ServeFile(w, r, s.BinaryPath)
}

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	agentID, token, err := s.svc.RedeemCode(r.Context(), strings.TrimSpace(body.Code), body.Name)
	if err != nil {
		// One message for every failure: an attacker must not learn whether the
		// code was wrong, expired or already used.
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid code"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "token": token})
}

func (s *Server) handlePoll(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing token"})
		return
	}
	job, err := s.svc.Poll(r.Context(), token)
	if errors.Is(err, ErrUnauthorized) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unknown agent"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	if job == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job_id": job.ID, "kind": job.Kind})
}

func (s *Server) handleResult(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
	var body struct {
		JobID  int64  `json:"job_id"`
		Output string `json:"output"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.svc.Complete(r.Context(), token, body.JobID, body.Output); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrUnauthorized) {
			status = http.StatusUnauthorized
		}
		writeJSON(w, status, map[string]string{"error": "rejected"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func bearer(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) > 7 && strings.EqualFold(value[:7], "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
