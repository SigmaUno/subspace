package hillclimb

import (
	"encoding/json"
	"net/http"

	"github.com/SigmaUno/subspace/internal/model"
)

type Handler struct {
	M   *Measurer
	mux *http.ServeMux
}

func NewHandler(m *Measurer) *Handler {
	h := &Handler{M: m, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /hillclimb/jobs", h.list)
	h.mux.HandleFunc("POST /hillclimb/jobs", h.create)
	h.mux.HandleFunc("GET /hillclimb/jobs/{id}/series", h.series)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

type createReq struct {
	BriefID   string   `json:"brief_id"`
	Repo      string   `json:"repo"`
	MetricCmd string   `json:"metric_cmd"`
	Direction string   `json:"direction"`
	Target    *float64 `json:"target"`
	Budget    int      `json:"budget"`
	PlateauN  int      `json:"plateau_n"`
}

func (h *Handler) list(w http.ResponseWriter, _ *http.Request) {
	js, err := h.M.Store.ListJobs()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": js})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		fail(w, model.BadRequest("invalid JSON body"))
		return
	}
	j := &Job{
		BriefID: req.BriefID, Repo: req.Repo, MetricCmd: req.MetricCmd, Direction: req.Direction,
		Target: req.Target, Budget: req.Budget, PlateauN: req.PlateauN,
	}
	if err := h.M.CreateJob(j); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, j)
}

func (h *Handler) series(w http.ResponseWriter, r *http.Request) {
	j, pts, err := h.M.Series(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": j, "points": pts})
}

type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	var b errBody
	b.Error.Code, b.Error.Message = e.Code, e.Message
	writeJSON(w, e.HTTPStatus(), b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
