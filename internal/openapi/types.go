package openapi

import (
	"github.com/SigmaUno/subspace/internal/autopilot"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/briefs"
	"github.com/SigmaUno/subspace/internal/components"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/quota"
)

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
}

type Error struct {
	Error ErrorDetail `json:"error"`
}

type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Paused  bool   `json:"paused"`
}

type Reason struct {
	Reason string `json:"reason"`
}

type Note struct {
	Note string `json:"note"`
}

type Paused struct {
	Paused bool `json:"paused"`
}

type Reopen struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

type Answer struct {
	Answer string `json:"answer"`
}

type ThreadPost struct {
	Body string `json:"body"`
}

type Expiry struct {
	ExpiresIn string `json:"expires_in"`
}

type CharterText struct {
	Text string `json:"text"`
}

type BackupResult struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    Free   `json:"data,omitempty"`
}

type RPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      Free   `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  Free   `json:"params,omitempty"`
}

type RPCResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      Free      `json:"id"`
	Result  Free      `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
}

type ActionForm struct {
	CSRF string `json:"csrf"`
}

type ActionLinkRequest struct {
	DecisionID string `json:"decision_id"`
	Action     string `json:"action"`
	Option     int    `json:"option"`
	TTL        string `json:"ttl,omitempty"`
}

type DecisionAction struct {
	Action    string `json:"action"`
	Answer    string `json:"answer,omitempty"`
	Note      string `json:"note,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Status    string `json:"status,omitempty"`
	Snooze    string `json:"snooze,omitempty"`
	ExpiresIn string `json:"expires_in,omitempty"`
}

type BulkRequest struct {
	IDs    []string `json:"ids"`
	Action string   `json:"action"`
	Params Free     `json:"params,omitempty"`
}

type BulkError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type BulkResult struct {
	ID      string         `json:"id"`
	OK      bool           `json:"ok"`
	Error   *BulkError     `json:"error,omitempty"`
	Connect *board.Connect `json:"connect,omitempty"`
}

type BulkResponse struct {
	Action    string       `json:"action"`
	Results   []BulkResult `json:"results"`
	Succeeded int          `json:"succeeded"`
	Failed    int          `json:"failed"`
}

type ImportResult struct {
	Brief      *model.Brief           `json:"brief,omitempty"`
	Components []components.Component `json:"components,omitempty"`
	Preview    *briefs.Doc            `json:"preview,omitempty"`
}

type Bot struct {
	*model.Agent
	Live bool `json:"live"`
}

type BotCreated struct {
	Bot     *model.Agent  `json:"bot"`
	Connect board.Connect `json:"connect"`
}

type BotInput struct {
	Name     string `json:"name,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	Count    int    `json:"count,omitempty"`
	Role     string `json:"role,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

type BotSpawn struct {
	BriefID  string `json:"brief_id"`
	Kind     string `json:"kind"`
	Model    string `json:"model"`
	N        int    `json:"n,omitempty"`
	Role     string `json:"role,omitempty"`
	Provider string `json:"provider,omitempty"`
}

type HillclimbCreate struct {
	BriefID   string   `json:"brief_id"`
	Repo      string   `json:"repo"`
	MetricCmd string   `json:"metric_cmd"`
	Direction string   `json:"direction"`
	Target    *float64 `json:"target,omitempty"`
	Budget    int      `json:"budget,omitempty"`
	PlateauN  int      `json:"plateau_n,omitempty"`
}

type ArtifactUpload struct {
	File        Binary `json:"file"`
	Scope       string `json:"scope,omitempty"`
	Name        string `json:"name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

type InstructionSave struct {
	Text    string `json:"text"`
	Message string `json:"message,omitempty"`
}

type InstructionRollback struct {
	To      int    `json:"to"`
	Message string `json:"message,omitempty"`
}

type NotifyStatus struct {
	Enabled bool     `json:"enabled"`
	Sinks   []string `json:"sinks"`
	notify.Stats
}

type AutopilotBrief struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Enabled bool   `json:"enabled"`
}

type AutopilotView struct {
	autopilot.Status
	Briefs []AutopilotBrief `json:"briefs"`
}

type AutopilotSet struct {
	Enabled bool `json:"enabled"`
}

type MergeEnqueue struct {
	TaskID string `json:"task_id"`
	PR     int    `json:"pr,omitempty"`
	Base   string `json:"base,omitempty"`
}

type Agent struct {
	*model.Agent
	Quota      *quota.Quota `json:"quota,omitempty"`
	QuotaUsage *quota.Today `json:"quota_usage,omitempty"`
}

type Me struct {
	Role  string `json:"role"`
	Scope string `json:"scope"`
}

type QuotaBulk struct {
	IDs   []string    `json:"ids"`
	Quota quota.Quota `json:"quota"`
}

type QuotaBulkResult struct {
	ID    string     `json:"id"`
	OK    bool       `json:"ok"`
	Error *BulkError `json:"error,omitempty"`
}

type QuotaBulkResponse struct {
	Results   []QuotaBulkResult `json:"results"`
	Succeeded int               `json:"succeeded"`
	Failed    int               `json:"failed"`
}

type KnowledgePin struct {
	Pinned *bool `json:"pinned,omitempty"`
}

type KnowledgeSupersede struct {
	By string `json:"by,omitempty"`
}

type LogLevel struct {
	Level string `json:"level"`
}
