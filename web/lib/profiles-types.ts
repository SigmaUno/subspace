export type AgentProfile = {
  name: string;
  role: string;
  kind: string;
  model: string;
  provider: string;
  expires_in: string;
  count: number;
  callback_url: string;
  description: string;
  created_at: string;
  updated_at: string;
};

export type ProfileList = { profiles: AgentProfile[] | null };
