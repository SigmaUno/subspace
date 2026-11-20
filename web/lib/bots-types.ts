import type { Agent, Connect } from "./types";

export type Bot = Agent & { capacity_weight?: number; spawned?: boolean };

export type BotRegistration = { agent: Bot; connect: Connect };

export type BotList = { agents: Bot[] | null };

export type BotAction = "pause" | "resume" | "revoke" | "rotate" | "spawn";
