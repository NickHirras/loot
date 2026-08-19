package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/nickhirras/loot/internal/core"
	"github.com/nickhirras/loot/internal/quests"
	"github.com/nickhirras/loot/internal/store"
)

// The quests and mysteries API. Four small endpoints and two POSTs that pay a
// drop; everything they need is computed in the packages behind them, so this
// file is only decoding, validating and answering.

// maxBody caps a request body: these are tiny JSON objects, and nothing here
// should ever read a megabyte.
const maxBody = 1 << 16

// handleQuests answers the board: active quests with fresh progress, and the
// last few that finished or quietly ended.
func (s *Server) handleQuests(w http.ResponseWriter, r *http.Request) {
	if s.Quests == nil {
		writeJSON(w, http.StatusOK, quests.Board{Active: []core.Quest{}, Recent: []core.Quest{}})
		return
	}
	board, err := s.Quests.List(r.Context())
	if err != nil {
		s.fail(w, "quests", err)
		return
	}
	writeJSON(w, http.StatusOK, board)
}

// questWindow is the `window` field of POST /api/quests. It accepts either a
// name ("week", "month") or an object with explicit days, because both are
// natural to type and neither is worth a second endpoint.
type questWindow struct {
	Name  string
	Start string
	End   string
}

func (qw *questWindow) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		qw.Name = name
		return nil
	}
	var explicit struct {
		Start string `json:"start"`
		End   string `json:"end"`
	}
	if err := json.Unmarshal(data, &explicit); err != nil {
		return errors.New(`window must be "week", "month", or {"start":"…","end":"…"}`)
	}
	qw.Name = "custom"
	qw.Start = explicit.Start
	qw.End = explicit.End
	return nil
}

// createQuestRequest is the body of POST /api/quests.
type createQuestRequest struct {
	Metric string      `json:"metric"`
	Target float64     `json:"target"`
	App    string      `json:"app"`
	Source string      `json:"source"`
	Window questWindow `json:"window"`
	Title  string      `json:"title"`
}

// handleQuestCreate sets a quest of your own. Custom quests are the escape
// hatch from the generator's opinions: any metric, any target, any window.
func (s *Server) handleQuestCreate(w http.ResponseWriter, r *http.Request) {
	if s.Quests == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "quests are not enabled"})
		return
	}

	var req createQuestRequest
	if r.Body != nil {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
	}

	quest, err := s.Quests.Create(r.Context(), quests.CustomRequest{
		Metric: core.Metric(strings.ToLower(strings.TrimSpace(req.Metric))),
		Target: req.Target,
		App:    req.App,
		Source: req.Source,
		Window: req.Window.Name,
		Start:  req.Window.Start,
		End:    req.Window.End,
		Title:  req.Title,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"quest": quest})
}

// handleQuestDelete removes a custom quest. A generated one is left alone:
// deleting it would only bring it back at midnight.
func (s *Server) handleQuestDelete(w http.ResponseWriter, r *http.Request) {
	if s.Quests == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "quests are not enabled"})
		return
	}
	err := s.Quests.Delete(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrQuestNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such quest"})
	case err != nil:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleMysteries answers the casebook: what is still unexplained, and the
// recently resolved with the notes that explain them.
func (s *Server) handleMysteries(w http.ResponseWriter, r *http.Request) {
	if s.Mysteries == nil {
		writeJSON(w, http.StatusOK, map[string]any{"open": []any{}, "resolved": []any{}})
		return
	}
	book, err := s.Mysteries.List(r.Context())
	if err != nil {
		s.fail(w, "mysteries", err)
		return
	}
	writeJSON(w, http.StatusOK, book)
}

// solveRequest is the body of POST /api/mysteries/{id}/solve.
type solveRequest struct {
	Note string `json:"note"`
}

// handleMysterySolve records your explanation and pays a drop for it.
func (s *Server) handleMysterySolve(w http.ResponseWriter, r *http.Request) {
	if s.Mysteries == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "mysteries are not enabled"})
		return
	}
	var req solveRequest
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req)
	}

	m, err := s.Mysteries.Solve(r.Context(), r.PathValue("id"), req.Note)
	if errors.Is(err, store.ErrMysteryNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such mystery"})
		return
	}
	if err != nil {
		s.fail(w, "mystery solve", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mystery": m})
}

// handleMysteryDismiss closes one quietly: no drop, no XP, no judgement.
func (s *Server) handleMysteryDismiss(w http.ResponseWriter, r *http.Request) {
	if s.Mysteries == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "mysteries are not enabled"})
		return
	}
	m, err := s.Mysteries.Dismiss(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrMysteryNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such mystery"})
		return
	}
	if err != nil {
		s.fail(w, "mystery dismiss", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mystery": m})
}
