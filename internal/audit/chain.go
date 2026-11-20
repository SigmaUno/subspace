package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

type Record struct {
	ID      int64
	TS      int64
	AgentID string
	Role    string
	Tool    string
	TaskID  string
	Before  string
	After   string
	Note    string
}

type canonical struct {
	ID      int64           `json:"id"`
	TS      int64           `json:"ts"`
	AgentID string          `json:"agent_id"`
	Role    string          `json:"role"`
	Tool    string          `json:"tool"`
	TaskID  string          `json:"task_id"`
	Before  json.RawMessage `json:"before"`
	After   json.RawMessage `json:"after"`
	Note    string          `json:"note"`
}

func Canonical(r Record) ([]byte, error) {
	before, err := canonJSON(r.Before)
	if err != nil {
		return nil, err
	}
	after, err := canonJSON(r.After)
	if err != nil {
		return nil, err
	}
	return json.Marshal(canonical{
		ID: r.ID, TS: r.TS, AgentID: r.AgentID, Role: r.Role, Tool: r.Tool,
		TaskID: r.TaskID, Before: before, After: after, Note: r.Note,
	})
}

func canonJSON(s string) (json.RawMessage, error) {
	if s == "" {
		return json.RawMessage("null"), nil
	}
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func Link(prev string, r Record) (string, error) {
	c, err := Canonical(r)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write(c)
	return hex.EncodeToString(h.Sum(nil)), nil
}

type Chain struct {
	HeadID   int64
	HeadHash string
	Events   int64
}

func (c *Chain) Add(r Record, hash string) bool {
	h, err := Link(c.HeadHash, r)
	if err != nil || h != hash || r.ID <= c.HeadID {
		return false
	}
	c.HeadID, c.HeadHash = r.ID, hash
	c.Events++
	return true
}
