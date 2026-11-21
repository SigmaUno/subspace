package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

var gatewayCommands = table{
	"list":    plain("gateways list", request{method: http.MethodGet, path: "gateways"}),
	"show":    byID("gateways show", "gateways/%s"),
	"tools":   byID("gateways tools", "gateways/%s/tools"),
	"add":     jsonBody("gateways add", http.MethodPost, "gateways", false),
	"update":  jsonBody("gateways update", http.MethodPut, "gateways/%s", true),
	"remove":  idCall("gateways remove", http.MethodDelete, "gateways/%s", nil),
	"enable":  gatewayToggle("gateways enable", true),
	"disable": gatewayToggle("gateways disable", false),
}

func gatewayToggle(name string, enabled bool) parser {
	return func(args []string, _ io.Reader) (command, error) {
		id, err := parseID(name, args)
		if err != nil {
			return nil, err
		}
		return func(ctx context.Context, c *conn, p *printer) error {
			resp, err := c.do(ctx, request{method: http.MethodGet, path: "gateways/" + id})
			if err != nil {
				return err
			}
			var body map[string]any
			if err := json.Unmarshal(resp.body, &body); err != nil {
				return fmt.Errorf("decode gateway: %w", err)
			}
			delete(body, "created_at")
			delete(body, "updated_at")
			body["enabled"] = enabled
			out, err := c.do(ctx, request{method: http.MethodPut, path: "gateways/" + id, body: body})
			if err != nil {
				return err
			}
			return p.response(out)
		}, nil
	}
}
