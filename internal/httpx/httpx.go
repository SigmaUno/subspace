package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
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
	WriteErrorStatus(w, model.AsError(err).HTTPStatus(), err)
}

func WriteErrorStatus(w http.ResponseWriter, status int, err error) {
	e := model.AsError(err)
	var b errBody
	b.Error.Code, b.Error.Message, b.Error.Details = e.Code, e.Message, e.Details
	WriteJSON(w, status, b)
}

func Fail(w http.ResponseWriter, log *slog.Logger, msg string, err error) {
	if log != nil && model.AsError(err).Code == "internal" {
		log.Error(msg, "err", err)
	}
	WriteError(w, err)
}

type Responder struct {
	Log  *slog.Logger
	Name string
}

func (p Responder) Fail(w http.ResponseWriter, err error) { Fail(w, p.Log, p.Name, err) }

func (p Responder) Reply(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		p.Fail(w, err)
		return
	}
	WriteJSON(w, status, v)
}

type capture struct {
	h    http.Header
	code int
	buf  bytes.Buffer
}

func (c *capture) Header() http.Header         { return c.h }
func (c *capture) WriteHeader(code int)        { c.code = code }
func (c *capture) Write(p []byte) (int, error) { return c.buf.Write(p) }

func Rewrite(w http.ResponseWriter, r *http.Request, next http.Handler, edit func(code int, body []byte) []byte) {
	c := &capture{h: http.Header{}, code: http.StatusOK}
	next.ServeHTTP(c, r)
	body := edit(c.code, c.buf.Bytes())
	for k, v := range c.h {
		w.Header()[k] = v
	}
	w.Header().Del("Content-Length")
	w.WriteHeader(c.code)
	_, _ = w.Write(body) //nolint:gosec // body is the wrapped handler's own JSON response
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

func DecodeStrict(w http.ResponseWriter, r *http.Request, max int64, v any) error {
	return decodeStrict(w, r, max, v, false)
}

func DecodeOptional(w http.ResponseWriter, r *http.Request, max int64, v any) error {
	return decodeStrict(w, r, max, v, true)
}

func decodeStrict(w http.ResponseWriter, r *http.Request, max int64, v any, optional bool) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, max))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if optional && errors.Is(err, io.EOF) {
			return nil
		}
		return model.BadRequest("invalid JSON body")
	}
	if dec.More() {
		return model.BadRequest("invalid JSON body")
	}
	return nil
}

func BoundedInt(v, name string, def, max int) (int, error) {
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > max {
		return 0, model.BadRequest("%s must be an integer between 1 and %d", name, max)
	}
	return n, nil
}

func Int(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(name))
	return n
}

func Int64(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	return n
}

func NonNegInt64(r *http.Request, name string) (int64, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, model.BadRequest("%s must be a non-negative integer", name)
	}
	return n, nil
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

func Reply(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, status, v)
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
