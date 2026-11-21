package httpx

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/SigmaUno/subspace/internal/model"
)

const MaxListLimit = 1000

type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details any    `json:"details,omitempty"`
	} `json:"error"`
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	var b errBody
	b.Error.Code, b.Error.Message, b.Error.Details = e.Code, e.Message, e.Details
	WriteJSON(w, e.HTTPStatus(), b)
}

func Fail(w http.ResponseWriter, log *slog.Logger, msg string, err error) {
	if log != nil && model.AsError(err).Code == "internal" {
		log.Error(msg, "err", err)
	}
	WriteError(w, err)
}

func Body(w http.ResponseWriter, r *http.Request, max int64) ([]byte, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, max))
	if err != nil {
		return nil, model.BadRequest("request body too large or unreadable")
	}
	return raw, nil
}

func Unmarshal(raw []byte, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return model.BadRequest("invalid JSON body")
	}
	return nil
}

func Decode(w http.ResponseWriter, r *http.Request, max int64, v any) error {
	raw, err := Body(w, r, max)
	if err != nil || len(raw) == 0 {
		return err
	}
	return Unmarshal(raw, v)
}

func Int(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(name))
	return n
}

func Int64(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	return n
}

func Limit(r *http.Request) int {
	return min(Int(r, "limit"), MaxListLimit)
}

func PositiveLimit(r *http.Request, def, max int) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, model.BadRequest("limit must be a positive integer")
	}
	return min(n, max), nil
}

func Page(key string, items any, next string) map[string]any {
	var cursor any
	if next != "" {
		cursor = next
	}
	return map[string]any{key: items, "next_cursor": cursor}
}

func Handle(status int, f func(r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := f(r)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, status, v)
	}
}
