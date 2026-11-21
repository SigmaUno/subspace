package briefs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/model"
)

type Importer struct {
	CreateBrief func(context.Context, board.BriefInput) (*model.Brief, error)
	SetPlan     func(context.Context, string, []components.Spec) ([]components.Component, error)
}

func (im Importer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /briefs/import", im.handleImport)
	mux.HandleFunc("GET /brief-templates", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"templates": Templates()})
	})
	mux.HandleFunc("GET /brief-templates/{id}/render", func(w http.ResponseWriter, r *http.Request) {
		vars := map[string]string{}
		for name, values := range r.URL.Query() {
			vars[name] = values[0]
		}
		md, err := Render(r.PathValue("id"), vars)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id"), "markdown": md})
	})
	return mux
}

func (im Importer) handleImport(w http.ResponseWriter, r *http.Request) {
	dry := false
	if q := r.URL.Query().Get("dry_run"); q != "" {
		var err error
		if dry, err = strconv.ParseBool(q); err != nil {
			writeError(w, model.BadRequest("dry_run must be true or false"))
			return
		}
	}
	src, err := readDocument(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	doc, err := Parse(src)
	if err != nil {
		writeError(w, err)
		return
	}
	if dry {
		writeJSON(w, http.StatusOK, map[string]any{"preview": doc})
		return
	}
	br, err := im.CreateBrief(r.Context(), doc.BriefInput())
	if err != nil {
		writeError(w, err)
		return
	}
	comps := []components.Component{}
	if len(doc.Components) > 0 {
		if comps, err = im.SetPlan(r.Context(), br.ID, doc.Components); err != nil {
			writeError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"brief": br, "components": comps})
}

func readDocument(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	limit := int64(MaxDocumentBytes)
	if mt == "application/json" {
		limit = 6*MaxDocumentBytes + 1024
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, model.Errf("too_large", "request body is larger than %d bytes", tooBig.Limit)
		}
		return nil, model.BadRequest("request body is unreadable")
	}
	if mt != "application/json" {
		return raw, nil
	}
	var in struct {
		Markdown *string `json:"markdown"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.More() {
		return nil, model.BadRequest(`invalid JSON body: expected {"markdown":"..."}`)
	}
	if in.Markdown == nil {
		return nil, model.BadRequest("markdown is required")
	}
	if len(*in.Markdown) > MaxDocumentBytes {
		return nil, model.Errf("too_large", "document is larger than %d bytes", MaxDocumentBytes)
	}
	return []byte(*in.Markdown), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	var pe *ParseError
	if errors.As(err, &pe) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{
			"code": "bad_request", "message": pe.Error(), "line": pe.Line, "column": pe.Col}})
		return
	}
	e := model.AsError(err)
	status := e.HTTPStatus()
	if e.Code == "too_large" {
		status = http.StatusRequestEntityTooLarge
	}
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}
