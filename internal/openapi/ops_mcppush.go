package openapi

func init() {
	Register(
		get("/mcp", "Server push stream of an MCP session (Streamable HTTP, text/event-stream): JSON-RPC notifications for tool list changes, subscribed resources and Subspace wake events. A second stream on the same session replaces the first", "").H("Mcp-Session-Id! Last-Event-ID").As(mediaSSE),
	)
}
