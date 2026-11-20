"use client";

import { useEffect, useRef, useState } from "react";
import { api, optionalResource, useResource, useSubmit, value } from "../../../lib/client";
import type { Component, Conversation } from "../../../lib/swarm";
import type { Agent } from "../../../lib/types";
import { Feedback } from "./components";
import { EmptyState, ResourceState } from "./states";

function ConversationPanel({ target, revision }: { target: string; revision: number }) {
  const [id, setID] = useState("");
  const [sent, setSent] = useState(false);
  const log = useRef<HTMLDivElement>(null);
  const storageKey = `subspace-conversation:${target}`;
  useEffect(() => {
    const saved = sessionStorage.getItem(storageKey);
    if (saved) queueMicrotask(() => setID(saved));
  }, [storageKey]);
  const conversation = useResource(id ? `conversations/${encodeURIComponent(id)}` : null, revision, optionalResource<Conversation>);
  const form = useSubmit();
  useEffect(() => {
    if (log.current) log.current.scrollTop = log.current.scrollHeight;
  }, [conversation.data]);

  return <>
    <div className="chat-log" role="log" aria-label="Conversation" ref={log} tabIndex={0}>
      <ResourceState error={conversation.error} loading={!!id && conversation.data === undefined} retry={conversation.refresh} rows={2} />
      {conversation.data?.messages?.map((message) => <article key={message.id}>
        <div className="message-meta"><strong>{message.author_id}</strong><time dateTime={message.created_at}>{new Date(message.created_at).toLocaleString()}</time></div>
        <p className="preserve">{message.body}</p>
      </article>)}
      {!id && <EmptyState>No messages yet.</EmptyState>}
      {id && conversation.data === null && <EmptyState>Conversation history is unavailable.</EmptyState>}
    </div>
    <form className="stack" onSubmit={(event) => form.submit(event, async (fields, element) => {
      const body = value(fields, "body");
      if (!body) return;
      const result = await api<{ conversation_id: string }>("messages", "POST", { to: target, body });
      if (result.conversation_id) {
        sessionStorage.setItem(storageKey, result.conversation_id);
        setID(result.conversation_id);
      }
      element.reset();
      setSent(true);
      conversation.refresh();
    })}>
      <label>Message<textarea name="body" rows={2} required /></label>
      <Feedback error={form.error} />
      <div className="actions"><button className="primary" disabled={form.busy}>Send message</button>{sent && <span className="meta" role="status">Sent</span>}</div>
    </form>
  </>;
}

export function OwnerChat({ briefID, agents, components, target, select, revision, close }: {
  briefID: string; agents: Agent[]; components: Component[]; target: string; select: (target: string) => void;
  revision: number; close: () => void;
}) {
  const options = new Set(["leader", `brief:${briefID}`, ...agents.map((agent) => `agent:${agent.id}`), ...components.map((item) => `component:${item.id}`)]);
  return <section className="panel stack owner-chat" aria-label="Owner chat">
    <div className="heading"><h2>Owner chat</h2><button type="button" onClick={close}>Close chat</button></div>
    <label>Send to<select value={target} onChange={(event) => select(event.target.value)}>
      <option value="leader">Architect</option><option value={`brief:${briefID}`}>All agents on this brief</option>
      {!options.has(target) && <option value={target}>{target}</option>}
      {!!components.length && <optgroup label="Components">{components.map((item) => <option key={item.id} value={`component:${item.id}`}>{item.name}</option>)}</optgroup>}
      {!!agents.length && <optgroup label="Agents">{agents.map((agent) => <option key={agent.id} value={`agent:${agent.id}`}>{agent.id} · {agent.model}</option>)}</optgroup>}
    </select></label>
    <ConversationPanel key={target} target={target} revision={revision} />
  </section>;
}
