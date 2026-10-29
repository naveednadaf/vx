package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"vx/internal/app"
	"vx/internal/config"
)

type Server struct {
	app *app.App
	mux *http.ServeMux
	srv *http.Server
}

func New(a *app.App) *Server {
	s := &Server{app: a, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /status", s.handleStatus)
	s.mux.HandleFunc("POST /mode", s.handleMode)
	s.mux.HandleFunc("POST /group", s.handleGroup)
	s.mux.HandleFunc("POST /add", s.handleAdd)
	s.mux.HandleFunc("DELETE /group/{name}", s.handleDeleteGroup)
	s.mux.HandleFunc("POST /refresh", s.handleRefresh)
	s.mux.HandleFunc("POST /ping", s.handlePing)
	s.mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	return s
}

func (s *Server) ListenAndServe(port int) error {
	s.srv = &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s.srv.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.app.Status())
}

type modeReq struct {
	Mode string `json:"mode"`
	Node int    `json:"node"`
}

func (s *Server) handleMode(w http.ResponseWriter, r *http.Request) {
	var req modeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.app.SetMode(req.Mode, req.Node); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, s.app.Status())
}

type groupReq struct {
	Name string `json:"name"`
}

func (s *Server) handleGroup(w http.ResponseWriter, r *http.Request) {
	var req groupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.app.SetGroup(req.Name); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, s.app.Status())
}

type addReq struct {
	Group   string   `json:"group"`
	Sources []string `json:"sources"`
}

func (s *Server) handleAdd(w http.ResponseWriter, r *http.Request) {
	var req addReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	srcs := classifyInputs(req.Sources)
	if len(srcs) == 0 {
		writeErr(w, 400, fmt.Errorf("no sources provided"))
		return
	}
	added, total, warnings, err := s.app.AddSources(req.Group, srcs, true)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "warnings": warnings})
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "added": added, "total": total, "warnings": warnings,
		"status": s.app.Status(),
	})
}

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.app.DeleteGroup(name); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, s.app.Status())
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	group := r.URL.Query().Get("group")
	var warnings []string
	if group != "" {
		warnings = s.app.RefreshGroup(group)
	} else {
		warnings = s.app.Refresh()
	}
	writeJSON(w, 200, map[string]any{"ok": true, "warnings": warnings, "status": s.app.Status()})
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	group := r.URL.Query().Get("group")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.app.ForcePing(ctx, group); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, s.app.Status())
}

// classifyInputs maps raw strings from the extension/CLI to config sources.
func classifyInputs(inputs []string) []config.Source {
	var out []config.Source
	for _, in := range inputs {
		in = strings.TrimSpace(in)
		if in == "" {
			continue
		}
		switch {
		case strings.HasPrefix(in, "vless://"):
			out = append(out, config.Source{Type: config.SourceVless, Value: in})
		case strings.HasPrefix(in, "http://"), strings.HasPrefix(in, "https://"):
			out = append(out, config.Source{Type: config.SourceSub, Value: in})
		default:
			// Treat anything else as a local file path.
			out = append(out, config.Source{Type: config.SourceFile, Value: in})
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]any{"ok": false, "error": err.Error()})
}
