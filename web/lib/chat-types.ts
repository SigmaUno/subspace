import type { Agent, Brief, Message } from "./types";

export interface Conversation {
  id: string;
  to: string;
  recipients: string[] | null;
  created_at: string;
  messages?: Message[] | null;
}

export interface ConversationList {
  conversations: Conversation[] | null;
}

export interface AgentList {
  agents: Agent[] | null;
}

export interface BriefList {
  briefs: Brief[] | null;
}
