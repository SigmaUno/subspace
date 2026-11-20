package artifacts

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	multipartOverhead = 1 << 20
	maxFieldBytes     = 1 << 10
)

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /artifacts", s.upload)
	mux.HandleFunc("GET /artifacts", s.list)
	mux.HandleFunc("GET /artifacts/{id}", s.download)
	mux.HandleFunc("DELETE /artifacts/{id}", s.remove)
	return mux
}

func (s *Service) DownloadHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{id}", s.signedDownload)
	return mux
}

func (s *Service) upload(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > s.cfg.MaxFileSize+multipartOverhead {
		s.fail(w, tooLarge(s.cfg.MaxFileSize))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxFileSize+multipartOverhead)
	mr, err := r.MultipartReader()
	if err != nil {
		s.fail(w, model.BadRequest("expected a multipart/form-data upload"))
		return
	}
	u := Upload{Scope: r.URL.Query().Get("scope"), Name: r.URL.Query().Get("name")}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			s.fail(w, model.BadRequest("multipart field \"file\" is required"))
			return
		}
		if err != nil {
			s.fail(w, uploadError(err, s.cfg.MaxFileSize))
			return
		}
		switch part.FormName() {
		case "scope":
			u.Scope, err = field(part)
		case "name":
			u.Name, err = field(part)
		case "content_type":
			u.ContentType, err = field(part)
		case "file":
			if u.Name == "" {
				u.Name = part.FileName()
			}
			if u.ContentType == "" {
				u.ContentType = part.Header.Get("Content-Type")
			}
			u.Body = part
			a, err := s.Put(r.Context(), model.Owner, u)
			if err != nil {
				s.fail(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, a)
			return
		}
		if err != nil {
			s.fail(w, uploadError(err, s.cfg.MaxFileSize))
			return
		}
	}
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			s.fail(w, model.BadRequest("limit must be a positive integer"))
			return
		}
		limit = min(n, 1000)
	}
	out, err := s.List(r.Context(), model.Owner, r.URL.Query().Get("scope"), limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": out})
}

func (s *Service) download(w http.ResponseWriter, r *http.Request) {
	a, f, err := s.Open(r.Context(), model.Owner, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = f.Close() }()
	serve(w, r, a, f)
}

func (s *Service) signedDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query()
	if !s.verify(id, q.Get("exp"), q.Get("sig")) {
		s.fail(w, model.Forbidden("invalid or expired download link"))
		return
	}
	a, err := s.meta(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	f, err := s.openBlob(a)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _ = f.Close() }()
	serve(w, r, a, f)
}

func (s *Service) remove(w http.ResponseWriter, r *http.Request) {
	if err := s.Delete(r.Context(), model.Owner, r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func serve(w http.ResponseWriter, r *http.Request, a *Artifact, f io.ReadSeeker) {
	h := w.Header()
	h.Set("Content-Type", a.ContentType)
	h.Set("Content-Disposition", contentDisposition(a.Name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("ETag", `"`+a.SHA256+`"`)
	http.ServeContent(w, r, "", a.CreatedAt, f)
}

func field(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxFieldBytes))
	return string(b), err
}

func uploadError(err error, maxFile int64) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return tooLarge(maxFile)
	}
	return model.BadRequest("malformed multipart body: %v", err)
}

func (s *Service) fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	if e.Code == "internal" {
		s.cfg.Logger.Error("artifacts error", "err", err)
	}
	writeJSON(w, statusOf(e), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func statusOf(e *model.Error) int {
	switch e.Code {
	case CodeTooLarge:
		return http.StatusRequestEntityTooLarge
	case CodeQuotaExceeded:
		return http.StatusInsufficientStorage
	}
	return e.HTTPStatus()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
