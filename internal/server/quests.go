package server

import (
	"encoding/json"
	"errors"
	"io"
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

// The two refusals the handlers raise themselves, rather than relaying from
// the quests package.
var (
	questsDisabled = &quests.Error{Code: quests.CodeQuestsDisabled, Message: "quests are not enabled"}
	questNotFound  = &quests.Error{Code: quests.CodeNotFound, Message: "no such quest"}
)

// writeQuestError answers a refused quest request.
//
// The English sentence is always there, because a curl and a log line both
// want words. A refusal that carries a code sends that too — and the offending
// input when its sentence quotes one — so a dashboard can write the refusal in
// its own language instead of relaying ours. An error with no code (a
// malformed body, say) is still answered, with `error` alone.
func writeQuestError(w http.ResponseWriter, status int, err error) {
	body := map[string]any{"error": err.Error()}
	var coded *quests.Error
	if errors.As(err, &coded) {
		body["code"] = coded.Code
		if coded.Value != "" {
			body["value"] = coded.Value
		}
	}
	writeJSON(w, status, body)
}

// handleQuests answers the board: active quests with fresh progress, and the
// last few that finished or quietly ended.
func (s *Server) handleQuests(w http.ResponseWriter, r *http.Request) {
	if s.Quests == nil {
		writeJSON(w, http.StatusOK, quests.Board{Active: []core.Quest{}, Recent: []core.Quest{}})
		return
	}
	// A scoped board shows this product's quests and the realm-wide ones; see
	// quests.Service.ListScoped.
	board, err := s.Quests.ListScoped(r.Context(), scopeOf(r))
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
		return &quests.Error{
			Code:    quests.CodeBadWindowJSON,
			Message: `window must be "week", "month", or {"start":"…","end":"…"}`,
		}
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
		writeQuestError(w, http.StatusServiceUnavailable, questsDisabled)
		return
	}

	var req createQuestRequest
	if r.Body != nil {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
			writeQuestError(w, http.StatusBadRequest, err)
			return
		}
	}

	// A custom quest keeps its own app: whatever the form said, or — when the
	// form said nothing and the page was scoped — the product being looked at,
	// which is what "set a goal for this app" means when you are already
	// looking at one app.
	app := strings.TrimSpace(req.App)
	if app == "" {
		app = scopeOf(r)
	}

	quest, err := s.Quests.Create(r.Context(), quests.CustomRequest{
		Metric: core.Metric(strings.ToLower(strings.TrimSpace(req.Metric))),
		Target: req.Target,
		App:    app,
		Source: req.Source,
		Window: req.Window.Name,
		Start:  req.Window.Start,
		End:    req.Window.End,
		Title:  req.Title,
	})
	if err != nil {
		writeQuestError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"quest": quest})
}

// handleQuestDelete removes a custom quest. A generated one is left alone:
// deleting it would only bring it back at midnight.
func (s *Server) handleQuestDelete(w http.ResponseWriter, r *http.Request) {
	if s.Quests == nil {
		writeQuestError(w, http.StatusServiceUnavailable, questsDisabled)
		return
	}
	err := s.Quests.Delete(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrQuestNotFound):
		writeQuestError(w, http.StatusNotFound, questNotFound)
	case err != nil:
		writeQuestError(w, http.StatusBadRequest, err)
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
	// A mystery row records the raw (source, app) the flagged day belonged to,
	// so the scope is applied here rather than in SQL.
	if scope := scopeOf(r); scope != "" {
		book.Open = s.mysteriesInScope(scope, book.Open)
		book.Resolved = s.mysteriesInScope(scope, book.Resolved)
	}
	writeJSON(w, http.StatusOK, book)
}

// mysteriesInScope keeps the flagged days that belong to one product.
func (s *Server) mysteriesInScope(scope string, list []core.Mystery) []core.Mystery {
	out := make([]core.Mystery, 0, len(list))
	for _, m := range list {
		if s.inScope(scope, m.Source, m.App) {
			out = append(out, m)
		}
	}
	return out
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
	// An empty body is fine — solving a mystery without writing anything down
	// is allowed — but a body that is *there* and unreadable is a mistake
	// worth reporting, rather than a note silently thrown away.
	var req solveRequest
	if r.Body != nil {
		err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req)
		if err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
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
