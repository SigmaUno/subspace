"use client";

import { useState } from "react";
import { api, commaList, useResource, useSubmit, value } from "../../../../lib/client";
import type { Gateway, GatewayList, GatewayRateLimit, GatewayTools } from "../../../../lib/gateways-types";
import { Feedback } from "../shared/components";
import { EmptyState, ResourceState } from "../shared/states";

const roles = ["leader", "worker", "tester", "reviewer"];

function gatewayPath(name: string) {
  return `gateways/${encodeURIComponent(name)}`;
}

function number(fields: FormData, key: string) {
  const raw = value(fields, key);
  return raw === "" ? 0 : Number(raw);
}

function parseRateLimits(input: string): GatewayRateLimit[] {
  return commaList(input).map((entry) => {
    const [tool = "", perMinute = "0"] = entry.split("=");
    return { tool: tool.trim(), per_minute: Number(perMinute) };
  });
}

function formatRateLimits(limits: GatewayRateLimit[]) {
  return limits.map((limit) => `${limit.tool}=${limit.per_minute}`).join(", ");
}

function GatewayForm({ gateway, commands, done, cancel }: {
  gateway?: Gateway; commands: string[]; done: () => void; cancel: () => void;
}) {
  const form = useSubmit();
  const [transport, setTransport] = useState<string>(gateway?.transport ?? "streamable-http");
  const stdio = transport === "stdio";
  return <form className="panel stack" data-write onSubmit={(event) => form.submit(event, async (fields) => {
    const body = {
      name: gateway?.name ?? value(fields, "name"), transport,
      url: stdio ? "" : value(fields, "url"), command: stdio ? value(fields, "command") : "",
      args: stdio ? value(fields, "args").split("\n").map((arg) => arg.trim()).filter(Boolean) : [],
      credential_env: value(fields, "credential_env"),
      allowed_roles: roles.filter((role) => fields.get(`role_${role}`) === "on"),
      allow_tools: commaList(value(fields, "allow_tools")), deny_tools: commaList(value(fields, "deny_tools")),
      rate_limits: parseRateLimits(value(fields, "rate_limits")),
      budget: { max_calls_per_day: number(fields, "max_calls_per_day"), max_result_bytes_per_day: number(fields, "max_result_bytes_per_day") },
      timeout_ms: number(fields, "timeout_ms"), max_result_bytes: number(fields, "max_result_bytes"),
      allow_private_hosts: !stdio && fields.get("allow_private_hosts") === "on",
      audit_arguments: fields.get("audit_arguments") === "on",
      enabled: fields.get("enabled") === "on",
    };
    if (gateway) await api<Gateway>(gatewayPath(gateway.name), "PUT", body);
    else await api<Gateway>("gateways", "POST", body);
    done();
  })}>
    <h3>{gateway ? `Edit ${gateway.name}` : "New gateway"}</h3>
    <div className="form-grid">
      <label>Name<input name="name" placeholder="docs" pattern="[a-z][a-z0-9\-]{0,15}" maxLength={16} required
        defaultValue={gateway?.name} readOnly={!!gateway} /></label>
      <label>Transport<select name="transport" value={transport} onChange={(event) => setTransport(event.target.value)}>
        <option value="streamable-http">Streamable HTTP</option>
        <option value="stdio" disabled={!commands.length && !stdio}>Stdio</option>
      </select></label>
      {!stdio && <label>URL<input name="url" type="url" placeholder="https://mcp.example.com/mcp" maxLength={2048} required
        defaultValue={gateway?.url} /></label>}
      {stdio && <label>Command<select name="command" defaultValue={gateway?.command} required>
        {commands.map((command) => <option key={command} value={command}>{command}</option>)}
      </select></label>}
      {stdio && <label>Arguments<textarea name="args" rows={3} placeholder="one argument per line"
        defaultValue={gateway?.args.join("\n")} /></label>}
      <label>Credential variable<input name="credential_env" placeholder="SUBSPACE_GATEWAY_DOCS_TOKEN" pattern="[A-Z_][A-Z0-9_]*"
        maxLength={64} defaultValue={gateway?.credential_env} /></label>
      <label>Allowed tools<input name="allow_tools" placeholder="*" defaultValue={gateway?.allow_tools.join(", ")} /></label>
      <label>Denied tools<input name="deny_tools" placeholder="delete_*, admin_*" defaultValue={gateway?.deny_tools.join(", ")} /></label>
      <label>Rate limits per minute<input name="rate_limits" placeholder="search*=30, *=120"
        defaultValue={gateway ? formatRateLimits(gateway.rate_limits) : ""} /></label>
      <label>Calls per day<input name="max_calls_per_day" type="number" min={0} placeholder="0 = unlimited"
        defaultValue={gateway?.budget.max_calls_per_day || ""} /></label>
      <label>Result bytes per day<input name="max_result_bytes_per_day" type="number" min={0} placeholder="0 = unlimited"
        defaultValue={gateway?.budget.max_result_bytes_per_day || ""} /></label>
      <label>Timeout (ms)<input name="timeout_ms" type="number" min={1000} max={120000} placeholder="30000"
        defaultValue={gateway?.timeout_ms} /></label>
      <label>Result size cap (bytes)<input name="max_result_bytes" type="number" min={1024} max={1048576} placeholder="262144"
        defaultValue={gateway?.max_result_bytes} /></label>
    </div>
    <fieldset className="actions">
      <legend>Roles that see the tools</legend>
      {roles.map((role) => <label key={role}><input name={`role_${role}`} type="checkbox"
        defaultChecked={gateway ? gateway.allowed_roles.includes(role) : role === "worker"} /> {role}</label>)}
    </fieldset>
    {!stdio && <label><input name="allow_private_hosts" type="checkbox" defaultChecked={gateway?.allow_private_hosts ?? false} /> Allow private and loopback hosts</label>}
    <label><input name="audit_arguments" type="checkbox" defaultChecked={gateway?.audit_arguments ?? false} /> Record redacted arguments in the audit log</label>
    <label><input name="enabled" type="checkbox" defaultChecked={gateway?.enabled ?? true} /> Enabled</label>
    <p className="meta">The credential variable is the name of an environment variable on the server, never the credential itself. Agents never see it.</p>
    <Feedback error={form.error} />
    <div className="actions">
      <button className="primary" disabled={form.busy}>{gateway ? "Save gateway" : "Add gateway"}</button>
      <button type="button" onClick={cancel}>Cancel</button>
    </div>
  </form>;
}

function ToolsResult({ result }: { result: GatewayTools }) {
  const tools = result.tools ?? [];
  return <div className="stack">
    {result.error && <p role="alert" className="meta">{result.error}</p>}
    {!tools.length && !result.error && <p className="meta">The upstream lists no tools.</p>}
    {!!tools.length && <ul className="stack">{tools.map((tool) => (
      <li key={tool.name}>
        <code>{tool.exposed_as}</code>{" "}
        <span className={`pill ${tool.allowed ? "active" : "revoked"}`}>
          {tool.allowed ? "exposed" : tool.refused ? `refused: ${tool.refused}` : "filtered"}</span>
        {!!tool.flags?.length && <span className="meta"> {tool.flags.join(", ")}</span>}
        <details><summary className="meta">Upstream description (data)</summary><pre>{tool.description}</pre></details>
      </li>
    ))}</ul>}
  </div>;
}

export function GatewaysView() {
  const gateways = useResource<GatewayList>("gateways", 0);
  const [editing, setEditing] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [tools, setTools] = useState<Record<string, GatewayTools>>({});
  const [loading, setLoading] = useState("");
  const list = gateways.data?.gateways ?? [];
  const commands = gateways.data?.commands ?? [];
  const current = list.find((gateway) => gateway.name === editing);

  async function remove(gateway: Gateway) {
    if (!window.confirm(`Remove gateway ${gateway.name}? Agents lose its tools at once.`)) return;
    setError("");
    try {
      await api<void>(gatewayPath(gateway.name), "DELETE");
      gateways.refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Request failed.");
    }
  }

  async function inspect(gateway: Gateway) {
    setError("");
    setLoading(gateway.name);
    try {
      const result = await api<GatewayTools>(`${gatewayPath(gateway.name)}/tools`);
      setTools((all) => ({ ...all, [gateway.name]: result }));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Request failed.");
    } finally {
      setLoading("");
    }
  }

  function finish() {
    setEditing(null);
    gateways.refresh();
  }

  return <section className="stack" aria-labelledby="gateways-heading">
    <div className="heading"><h2 id="gateways-heading">MCP gateways</h2>
      <button data-write onClick={() => setEditing(editing === "" ? null : "")}>{editing === "" ? "Close form" : "New gateway"}</button></div>
    <p className="meta">Gateways expose approved external MCP servers to agents as <code>gw_&lt;gateway&gt;__&lt;tool&gt;</code> tools,
      filtered by role and tool globs, rate limited, budgeted, redacted and audited. Agents never hold the upstream credentials.
      {commands.length ? ` Allowed stdio commands: ${commands.join(", ")}.` : " No stdio commands are allowed (SUBSPACE_GATEWAY_COMMANDS)."}</p>
    {editing === "" && <GatewayForm commands={commands} done={finish} cancel={() => setEditing(null)} />}
    {current && <GatewayForm key={current.name} gateway={current} commands={commands} done={finish} cancel={() => setEditing(null)} />}
    <Feedback error={error} />
    <ResourceState error={gateways.error} loading={gateways.data === undefined} retry={gateways.refresh} rows={3} />
    {gateways.data && !list.length && <EmptyState>No gateways configured. Agents only see Subspace tools.</EmptyState>}
    {!!list.length && <div className="table-wrap">
      <table>
        <thead><tr><th>Gateway</th><th>Upstream</th><th>Access</th><th>Limits</th><th>Actions</th></tr></thead>
        <tbody>{list.map((gateway) => (
          <tr key={gateway.name}>
            <th scope="row">{gateway.name}
              <div className="meta"><span className={`pill ${gateway.enabled ? "active" : ""}`}>{gateway.enabled ? "enabled" : "disabled"}</span></div></th>
            <td>{gateway.transport}
              <div className="meta">{gateway.transport === "stdio" ? [gateway.command, ...gateway.args].join(" ") : gateway.url}</div>
              <div className="meta">{gateway.credential_env ? `credential ${gateway.credential_env}` : "no credential"}
                {gateway.allow_private_hosts && " · private hosts allowed"}</div></td>
            <td>{gateway.allowed_roles.join(", ")}
              <div className="meta">allow {gateway.allow_tools.join(", ")}{gateway.deny_tools.length ? ` · deny ${gateway.deny_tools.join(", ")}` : ""}</div></td>
            <td>{gateway.rate_limits.length ? formatRateLimits(gateway.rate_limits) + " per minute" : "no rate limits"}
              <div className="meta">{gateway.budget.max_calls_per_day ? `${gateway.budget.max_calls_per_day} calls per day` : "unlimited calls"}
                {" · "}timeout {gateway.timeout_ms} ms · results up to {gateway.max_result_bytes} bytes</div></td>
            <td><div className="actions">
              <button disabled={loading === gateway.name} onClick={() => inspect(gateway)}>Show tools</button>
              <span className="actions" data-write>
                <button onClick={() => setEditing(gateway.name)}>Edit</button>
                <button className="danger" onClick={() => remove(gateway)}>Remove</button>
              </span>
            </div>
            {tools[gateway.name] && <ToolsResult result={tools[gateway.name]} />}</td>
          </tr>
        ))}</tbody>
      </table>
    </div>}
  </section>;
}
