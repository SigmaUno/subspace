"use client";

import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { api, label, useResource, useSubmit } from "../../../lib/client";
import type { AgentList, BriefList, Conversation, ConversationList } from "../../../lib/chat-types";
import type { Message } from "../../../lib/types";
import { Feedback } from "./components";
import { useLive } from "./live";

const readKey = "subspace.chat.read";

function readState(): Record<string, number> {
  if (typeof window === "undefined") return {};
  try {
    return JSON.parse(window.localStorage.getItem(readKey) ?? "{}");
  } catch {
    return {};
  }
}

function newestID(messages: Message[] | null | undefined) {
  return Math.max(0, ...(messages ?? []).map((message) => message.id));
}

function unreadCount(conversation: Conversation, read: Record<string, number>) {
  const seen = read[conversation.id] ?? 0;
  return (conversation.messages ?? []).filter((message) => message.author_role !== "owner" && message.id > seen).length;
}

async function loadConversations(path: string, signal: AbortSignal) {
  const { conversations } = await api<ConversationList>(path, "GET", undefined, signal);
  return Promise.all((conversations ?? []).map((item) =>
    api<Conversation>(`conversations/${encodeURIComponent(item.id)}`, "GET", undefined, signal)));
}

export function ChatScreen() {
  const { revisions, connected } = useLive("all");
  const conversations = useResource<Conversation[]>("conversations", revisions.thread, loadConversations);
  const agents = useResource<AgentList>("agents?limit=1000", revisions.agents);
  const briefs = useResource<BriefList>("briefs?limit=1000", revisions.briefs);
  const items = conversations.data ?? [];

  const [selectedID, setSelectedID] = useState<string>();
  const selected = items.find((conversation) => conversation.id === selectedID) ?? items[0];
  const [read, setRead] = useState(readState);
  const [target, setTarget] = useState("leader");
  const [draft, setDraft] = useState("");
  const form = useSubmit();
  const log = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (log.current) log.current.scrollTop = log.current.scrollHeight;
  }, [selected?.id, selected?.messages?.length]);

  function markRead(conversation: Conversation | undefined) {
    if (!conversation) return;
    const latest = newestID(conversation.messages);
    if ((read[conversation.id] ?? 0) >= latest) return;
    const next = { ...read, [conversation.id]: latest };
    window.localStorage.setItem(readKey, JSON.stringify(next));
    setRead(next);
  }

  function select(conversation: Conversation) {
    if (conversation.id !== selected?.id) markRead(selected);
    setSelectedID(conversation.id);
  }

  const targets = [
    { to: "leader", label: "Leader" },
    ...(agents.data?.agents ?? [])
      .filter((agent) => agent.status !== "revoked")
      .map((agent) => ({ to: `agent:${agent.id}`, label: `Agent · ${agent.id}` })),
    ...(briefs.data?.briefs ?? []).map((brief) => ({ to: `brief:${brief.id}`, label: `Brief · ${brief.title}` })),
  ];

  return (
    <>
      <header className="topbar">
        <Link className="brand" href="/briefs"><span className="brand-mark" aria-hidden="true" />Subspace</Link>
        <nav aria-label="Workspace">
          <Link href="/briefs">Workspace</Link>
          <Link href="/chat" aria-current="page">Chat</Link>
        </nav>
        <div className="top-actions">
          <span className={`connection ${connected ? "online" : ""}`} role="status">{connected ? "Live" : "Reconnecting"}</span>
        </div>
      </header>
      <main className="workspace wide chat-screen">
        <section className="panel chat-list" aria-label="Conversations">
          <h2>Conversations</h2>
          <Feedback error={conversations.error} loading={!conversations.data} />
          {conversations.data?.length === 0 && <p className="empty">No conversations yet.</p>}
          <ul>
            {items.map((conversation) => {
              const unread = conversation.id === selected?.id ? 0 : unreadCount(conversation, read);
              return (
                <li key={conversation.id}>
                  <button className="chat-item" aria-current={conversation.id === selected?.id ? "true" : undefined}
                    onClick={() => select(conversation)}>
                    <span className="break">{conversation.to}</span>
                    {unread > 0 && <span className="nav-count" aria-label={`${unread} unread`}>{unread}</span>}
                    <span className="meta">{new Date(conversation.created_at).toLocaleString()}</span>
                  </button>
                </li>
              );
            })}
          </ul>
        </section>
        <section className="panel chat-pane" aria-label="Conversation">
          {selected ? (
            <>
              <header className="chat-head">
                <div className="meta">Conversation · {selected.id}</div>
                <h2 className="break">{selected.to}</h2>
              </header>
              <div className="messages" role="log" aria-label="Messages" tabIndex={0} ref={log}>
                {selected.messages?.length === 0 && <p className="empty">No messages yet.</p>}
                {selected.messages?.map((message) => (
                  <article key={message.id}>
                    <div className="message-meta">
                      <strong>{message.author_role === "owner" ? "You" : message.author_id}</strong>
                      <span>{message.author_role} · {label(message.kind)}</span>
                      <time dateTime={message.created_at}>{new Date(message.created_at).toLocaleString()}</time>
                    </div>
                    <p className="preserve">{message.body}</p>
                  </article>
                ))}
              </div>
            </>
          ) : (
            <p className="empty">Select a conversation or send a new message.</p>
          )}
          <form className="stack reply" onSubmit={(event) => form.submit(event, async () => {
            const body = draft.trim();
            if (!body) throw new Error("Enter a message.");
            const conversation = await api<Conversation>("messages", "POST", { to: target, body });
            setDraft("");
            markRead(selected);
            setSelectedID(conversation.id);
            conversations.refresh();
          })}>
            <label>To
              <select value={target} onChange={(event) => setTarget(event.target.value)}>
                {targets.map((option) => <option key={option.to} value={option.to}>{option.label}</option>)}
              </select>
            </label>
            <label>Message
              <textarea name="body" required rows={3} value={draft} onChange={(event) => setDraft(event.target.value)} />
            </label>
            <Feedback error={form.error || conversations.error} />
            <div><button className="primary" disabled={form.busy}>Send message</button></div>
          </form>
        </section>
      </main>
    </>
  );
}
