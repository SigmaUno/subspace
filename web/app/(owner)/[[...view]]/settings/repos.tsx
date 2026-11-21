"use client";

import { useState } from "react";
import { api, commaList, useResource, useSubmit, value } from "../../../../lib/client";
import type { Repo, RepoHealth, RepoList } from "../../../../lib/repos-types";
import { Feedback } from "../shared/components";
import { EmptyState, ResourceState } from "../shared/states";

const hosts = [["github", "GitHub"], ["forgejo", "Forgejo"]];

function repoPath(name: string) {
  return `repos/${name.split("/").map(encodeURIComponent).join("/")}`;
}

function RepoForm({ repo, done, cancel }: { repo?: Repo; done: () => void; cancel: () => void }) {
  const form = useSubmit();
  return <form className="panel stack" data-write onSubmit={(event) => form.submit(event, async (fields) => {
    const body = {
      name: repo?.name ?? value(fields, "name"), host: value(fields, "host"), base_url: value(fields, "base_url"),
      default_branch: value(fields, "default_branch"), protected_branches: commaList(value(fields, "protected_branches")),
      merge_test_command: value(fields, "merge_test_command").split(/\s+/).filter(Boolean),
      merge_work_dir: value(fields, "merge_work_dir"), token_env: value(fields, "token_env"),
      enabled: fields.get("enabled") === "on",
    };
    if (repo) await api<Repo>(repoPath(repo.name), "PUT", body);
    else await api<Repo>("repos", "POST", body);
    done();
  })}>
    <h3>{repo ? `Edit ${repo.name}` : "New repository"}</h3>
    <div className="form-grid">
      <label>Name<input name="name" placeholder="owner/name" pattern="[A-Za-z0-9_.\-]+/[A-Za-z0-9_.\-]+" maxLength={200} required
        defaultValue={repo?.name} readOnly={!!repo} /></label>
      <label>Host<select name="host" defaultValue={repo?.host ?? "github"}>
        {hosts.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
      </select></label>
      <label>Base URL<input name="base_url" type="url" placeholder="https://github.com" maxLength={2048} defaultValue={repo?.base_url} /></label>
      <label>Default branch<input name="default_branch" placeholder="main" maxLength={100} defaultValue={repo?.default_branch} /></label>
      <label>Protected branches<input name="protected_branches" placeholder="main, release/*"
        defaultValue={repo?.protected_branches.join(", ")} /></label>
      <label>Token variable<input name="token_env" placeholder="GITHUB_TOKEN" pattern="[A-Z_][A-Z0-9_]*" maxLength={64}
        defaultValue={repo?.token_env} /></label>
      <label>Merge test command<input name="merge_test_command" placeholder="make test"
        defaultValue={repo?.merge_test_command.join(" ")} /></label>
      <label>Merge work directory<input name="merge_work_dir" placeholder="/data/mergequeue" maxLength={500}
        defaultValue={repo?.merge_work_dir} /></label>
    </div>
    <label><input name="enabled" type="checkbox" defaultChecked={repo?.enabled ?? true} /> Enabled</label>
    <p className="meta">The token variable is the name of an environment variable on the server, never the token itself.</p>
    <Feedback error={form.error} />
    <div className="actions">
      <button className="primary" disabled={form.busy}>{repo ? "Save repository" : "Add repository"}</button>
      <button type="button" onClick={cancel}>Cancel</button>
    </div>
  </form>;
}

function HealthResult({ health }: { health: RepoHealth }) {
  return <div role="status" className="meta">
    <span className={`pill ${health.ok ? "active" : "revoked"}`}>{health.ok ? "healthy" : "unhealthy"}</span>{" "}
    {health.reachable ? "reachable" : "not reachable"} · default branch {health.default_branch}
    {health.default_branch_exists ? " found" : " missing"}
    {health.host_default_branch && health.host_default_branch !== health.default_branch && ` (host default: ${health.host_default_branch})`}
    <div>{health.token_hint}</div>
    {health.error && <div>{health.error}</div>}
  </div>;
}

export function RepositoriesView() {
  const repos = useResource<RepoList>("repos", 0);
  const [editing, setEditing] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [health, setHealth] = useState<Record<string, RepoHealth>>({});
  const [checking, setChecking] = useState("");
  const list = repos.data?.repos ?? [];
  const current = list.find((repo) => repo.name === editing);

  async function remove(repo: Repo) {
    if (!window.confirm(`Remove repository ${repo.name} from the registry?`)) return;
    setError("");
    try {
      await api<void>(repoPath(repo.name), "DELETE");
      repos.refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Request failed.");
    }
  }

  async function check(repo: Repo) {
    setError("");
    setChecking(repo.name);
    try {
      const result = await api<RepoHealth>(`${repoPath(repo.name)}/health`);
      setHealth((all) => ({ ...all, [repo.name]: result }));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Request failed.");
    } finally {
      setChecking("");
    }
  }

  function finish() {
    setEditing(null);
    repos.refresh();
  }

  return <section className="stack" aria-labelledby="repos-heading">
    <div className="heading"><h2 id="repos-heading">Repositories</h2>
      <button data-write onClick={() => setEditing(editing === "" ? null : "")}>{editing === "" ? "Close form" : "New repository"}</button></div>
    <p className="meta">While the registry has repositories, briefs and tasks may only use these. Pull request sync, forge checks, review comments and the merge queue read it when the server starts.</p>
    {editing === "" && <RepoForm done={finish} cancel={() => setEditing(null)} />}
    {current && <RepoForm key={current.name} repo={current} done={finish} cancel={() => setEditing(null)} />}
    <Feedback error={error} />
    <ResourceState error={repos.error} loading={repos.data === undefined} retry={repos.refresh} rows={3} />
    {repos.data && !list.length && <EmptyState>No repositories registered. Any repository is accepted.</EmptyState>}
    {!!list.length && <div className="table-wrap" role="region" aria-label="Repositories" tabIndex={0}>
      <table>
        <thead><tr><th>Repository</th><th>Host</th><th>Branches</th><th>Merge queue</th><th>Actions</th></tr></thead>
        <tbody>{list.map((repo) => (
          <tr key={repo.name}>
            <th scope="row">{repo.name}
              <div className="meta"><span className={`pill ${repo.enabled ? "active" : ""}`}>{repo.enabled ? "enabled" : "disabled"}</span></div></th>
            <td>{repo.host}<div className="meta">{repo.base_url}</div>
              <div className="meta">{repo.token_env ? `token ${repo.token_env}` : "no token"}</div></td>
            <td>{repo.default_branch}<div className="meta">{repo.protected_branches.length ? `protected ${repo.protected_branches.join(", ")}` : "none protected"}</div></td>
            <td>{repo.merge_test_command.length ? repo.merge_test_command.join(" ") : "—"}
              {repo.merge_work_dir && <div className="meta">{repo.merge_work_dir}</div>}</td>
            <td><div className="actions">
              <button disabled={checking === repo.name} onClick={() => check(repo)}>Check health</button>
              <span className="actions" data-write>
                <button onClick={() => setEditing(repo.name)}>Edit</button>
                <button className="danger" onClick={() => remove(repo)}>Remove</button>
              </span>
            </div>
            {health[repo.name] && <HealthResult health={health[repo.name]} />}</td>
          </tr>
        ))}</tbody>
      </table>
    </div>}
  </section>;
}
