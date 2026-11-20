package spawner

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

func MCPURL(publicURL string) string {
	return strings.TrimRight(publicURL, "/") + "/mcp"
}

func JoinLine(publicURL, agentID string) string {
	return "Join Subspace at " + strings.TrimRight(publicURL, "/") + " as " + agentID + ". Call get_instructions and follow them."
}

func Snippets(publicURL, token string) (claudeCode, codexTOML, opencodeJSON string) {
	if token == "" {
		token = "<token>"
	}
	url := MCPURL(publicURL)
	bearer := "Bearer " + token

	claudeCode = "claude mcp add --transport http subspace " + url + " --header \"Authorization: " + bearer + "\""
	codexTOML = "[mcp_servers.subspace]\nurl = " + strconv.Quote(url) + "\nhttp_headers = { Authorization = " + strconv.Quote(bearer) + " }\n"
	opencodeJSON = `{"mcp":{"subspace":{"type":"remote","url":` + jsonString(url) +
		`,"headers":{"Authorization":` + jsonString(bearer) + `}}}}`
	return claudeCode, codexTOML, opencodeJSON
}

func jsonString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}
