package openapi

import "github.com/SigmaUno/subspace/internal/gateway"

func init() {
	Register(
		get("/api/v1/gateways", "List MCP gateways and the stdio commands the server allows", gateway.List{}),
		post("/api/v1/gateways", "Add an MCP gateway", gateway.Input{}, gateway.Gateway{}).S(created),
		get("/api/v1/gateways/{name}", "Get an MCP gateway", gateway.Gateway{}),
		put("/api/v1/gateways/{name}", "Replace an MCP gateway", gateway.Input{}, gateway.Gateway{}),
		del("/api/v1/gateways/{name}", "Remove an MCP gateway", nil).S(gone),
		get("/api/v1/gateways/{name}/tools", "Fetch the upstream tools of a gateway with the vetting result of each", gateway.Tools{}),
	)
}
