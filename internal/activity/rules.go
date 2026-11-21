package activity

import "github.com/SigmaUno/subspace/internal/model"

const (
	SevInfo    = "info"
	SevSuccess = "success"
	SevWarning = "warning"
	SevError   = "error"
)

type rule struct {
	icon     string
	severity string
	text     string
	onTask   string
	byStatus map[string]string
	warnIf   string
}

var fallback = rule{icon: "activity", severity: SevInfo, text: "{actor} used {tool}{task? on }"}

var rules = map[string]rule{
	"redeliver_webhook_out": {icon: "webhook", text: "{actor} redelivered outbound webhook delivery {a.id}"},
	"create_webhook_out":    {icon: "webhook", text: "{actor} created outbound webhook {a.id}"},
	"delete_webhook_out":    {icon: "webhook", severity: SevWarning, text: "{actor} deleted outbound webhook {a.id}"},
	"delete_announcement":   {icon: "megaphone", severity: SevWarning, text: "{actor} deleted announcement {a.id}"},
	"publish_announcement":  {icon: "megaphone", text: "{actor} published announcement {a.id}{note?: }"},
	"retention_archive":     {icon: "archive", text: "{actor} archived old data ({a.rows} rows)"},
	"retention_restore":     {icon: "archive", severity: SevWarning, text: "{actor} restored archived data"},
	"set_agent_quota":       {icon: "gauge", text: "{actor} set quota for agent {a.agent}"},
	"set_log_level":         {icon: "log", text: "{actor} set log level to {a.level}"},
	"add_knowledge":         {icon: "book", text: "{actor} added knowledge entry {a.id}{note?: }"},
	"approve_knowledge":     {icon: "check", text: "{actor} approved knowledge entry {a.id}"},
	"create_agent_profile":  {icon: "bot", text: "{actor} created agent profile {a.name}"},
	"delete_agent_profile":  {icon: "bot", severity: SevWarning, text: "{actor} deleted agent profile {b.name}"},
	"update_agent_profile":  {icon: "bot", text: "{actor} updated agent profile {a.name}"},
	"set_autopilot_spawn":   {icon: "settings", text: "{actor} changed the autopilot spawn options of {a.brief_id}"},
	"create_token":          {icon: "key", text: "{actor} created API token {a.id}{note?: }"},
	"delete_knowledge":      {icon: "x", severity: SevWarning, text: "{actor} deleted knowledge entry {a.id}"},
	"pin_knowledge":         {icon: "book", text: "{actor} pinned knowledge entry {a.id}"},
	"revoke_token":          {icon: "x", severity: SevWarning, text: "{actor} revoked API token {a.id}"},
	"supersede_knowledge":   {icon: "book", text: "{actor} superseded knowledge entry {a.id}"},
	"update_knowledge":      {icon: "book", text: "{actor} updated knowledge entry {a.id}"},
	"accept_split":          {icon: "split", text: "{actor} accepted split {a.request_id} of {task} into {a.created}"},
	"add_plan_task":         {icon: "plan", text: "{actor} added {task} to plan {a.plan_id}"},
	"answer_question":       {icon: "question", text: "{actor} answered question {a.question_id}"},
	"approve_plan":          {icon: "check", severity: SevSuccess, text: "{actor} approved plan {a.plan_id}{note?: }"},
	"ask_owner":             {icon: "question", severity: SevWarning, text: "{actor} asked a question ({a.question_id}){task? on }"},
	"assign":                {icon: "user", text: "{actor} assigned {task}{a.assignee? to }"},
	"block":                 {icon: "alert", severity: SevWarning, text: "{actor} blocked {task}{note?: }"},
	"bulk_create_bots":      {icon: "bot", text: "{actor} created {a.count} bots starting at {a.first}"},
	"cancel":                {icon: "x", severity: SevWarning, text: "{actor} cancelled {task}{note?: }"},
	"claim_task":            {icon: "play", text: "{actor} claimed {task}"},
	"close_brief":           {icon: "folder", severity: SevWarning, text: "{actor} closed brief {a.brief_id} ({a.cancelled} cancelled, {a.blocked} blocked)", onTask: "{actor} closed the brief: {task} is now {status}"},
	"create_bot":            {icon: "bot", text: "{actor} created bot {a.bot}"},
	"create_brief":          {icon: "folder", text: "{actor} created brief {a.brief_id}{note?: }"},
	"create_task":           {icon: "plus", text: "{actor} created {task}{note?: }"},
	"delete_artifact":       {icon: "file", severity: SevWarning, text: "{actor} deleted artifact {b.id} ({b.name})"},
	"delete_bot":            {icon: "bot", severity: SevWarning, text: "{actor} deleted bot {a.bot}"},
	"drop_plan_task":        {icon: "plan", text: "{actor} dropped {task} from plan {a.plan_id}"},
	"edit_charter":          {icon: "settings", text: "{actor} edited the charter"},
	"edit_task":             {icon: "edit", text: "{actor} edited {task}"},
	"instructions_rollback": {icon: "settings", text: "{actor} rolled back instructions ({note}) to version {a.version}"},
	"instructions_save":     {icon: "settings", text: "{actor} saved instructions ({note}) as version {a.version}"},
	"kill_bot":              {icon: "stop", severity: SevWarning, text: "{actor} killed bot {a.bot}{note?: }"},
	"lease_release":         {icon: "pause", severity: SevWarning, text: "{actor} released the lease on {task}{note?: }"},
	"mark_done":             {icon: "check", severity: SevSuccess, text: "{actor} marked {task} done{note?: }"},
	"pause_agent":           {icon: "pause", severity: SevWarning, text: "{actor} paused agent {b.agent}"},
	"pause_all":             {icon: "pause", severity: SevWarning, text: "{actor} paused all agents{note?: }"},
	"pause_bot":             {icon: "pause", severity: SevWarning, text: "{actor} paused bot {a.bot}{note?: }"},
	"propose_plan":          {icon: "plan", text: "{actor} proposed plan {a.plan_id} with {a.tasks} tasks"},
	"put_artifact":          {icon: "file", text: "{actor} uploaded artifact {a.id} ({a.name})"},
	"reassign":              {icon: "user", text: "{actor} reassigned {task}{a.assignee? to }{note?: }"},
	"register_agents":       {icon: "bot", text: "{actor} registered {a.count} agents starting at {a.first}"},
	"reject_plan":           {icon: "x", severity: SevWarning, text: "{actor} rejected plan {a.plan_id}{note?: }"},
	"reopen":                {icon: "play", text: "{actor} reopened {task}{note?: }"},
	"reopen_brief":          {icon: "folder", text: "{actor} reopened brief {a.brief_id}"},
	"report_blocker":        {icon: "alert", severity: SevWarning, text: "{actor} reported a blocker on {task} (needs {a.needs})"},
	"report_tests":          {icon: "check", text: "{actor} reported tests on {task}: {a.passed} passed, {a.failed} failed", warnIf: "a.failed"},
	"request_help":          {icon: "question", severity: SevWarning, text: "{actor} requested help on {task} ({a.kind})"},
	"request_spawn":         {icon: "spawn", text: "{actor} requested {a.n} {a.kind} agents for brief {a.brief_id}"},
	"resume_agent":          {icon: "play", text: "{actor} resumed agent {b.agent}"},
	"resume_all":            {icon: "play", text: "{actor} resumed all agents"},
	"resume_bot":            {icon: "play", text: "{actor} resumed bot {a.bot}"},
	"review": {icon: "comment", text: "{actor} reviewed {task}", byStatus: map[string]string{
		model.StatusApproved:         "{actor} approved {task}{note?: }",
		model.StatusChangesRequested: "{actor} requested changes on {task}{note?: }",
		model.StatusNeedsOwner:       "{actor} escalated {task} to the owner after repeated reviews",
	}},
	"revoke_agent":     {icon: "x", severity: SevWarning, text: "{actor} revoked agent {b.agent}"},
	"revoke_bot":       {icon: "x", severity: SevWarning, text: "{actor} revoked bot {b.bot}{note?: }"},
	"rotate_token":     {icon: "lock", text: "{actor} rotated the token of agent {b.agent}"},
	"set_brief_leader": {icon: "folder", text: "{actor} changed the leader of brief {a.brief_id}{a.leader_id? to }"},
	"set_budget":       {icon: "budget", text: "{actor} changed the budget of brief {a.brief_id}", onTask: "{actor} changed the budget of {task}"},
	"set_capacity":     {icon: "settings", text: "{actor} set capacity to {a.global_max_agents} agents and {a.limits} limits"},
	"set_limits":       {icon: "budget", text: "{actor} set spend limits to {a.daily_cost_eur} per day and {a.weekly_cost_eur} per week"},
	"spawn_bot":        {icon: "spawn", text: "{actor} spawned bot {a.bot}{note?: }"},
	"stop_spawn":       {icon: "stop", text: "{actor} stopped spawn {a.spawn_id} ({a.reason})"},
	"submit":           {icon: "send", severity: SevSuccess, text: "{actor} submitted {task}{pr? (PR #|)}"},
	"suggest_split":    {icon: "split", text: "{actor} suggested splitting {task} into {a.tasks} tasks"},
	"update_status":    {icon: "activity", text: "{actor} updated {task}{a.progress_pct?: |% done}{status?: now |}{note?: }"},
	"update_task":      {icon: "edit", text: "{actor} updated {task}{status?: now |}"},
}
