export type GatewayRateLimit = { tool: string; per_minute: number };

export type GatewayBudget = { max_calls_per_day: number; max_result_bytes_per_day: number };

export type Gateway = {
  name: string;
  transport: "streamable-http" | "stdio";
  url: string;
  command: string;
  args: string[];
  credential_env: string;
  allowed_roles: string[];
  allow_tools: string[];
  deny_tools: string[];
  rate_limits: GatewayRateLimit[];
  budget: GatewayBudget;
  timeout_ms: number;
  max_result_bytes: number;
  allow_private_hosts: boolean;
  audit_arguments: boolean;
  enabled: boolean;
  created_at: string;
  updated_at: string;
};

export type GatewayList = { gateways: Gateway[] | null; commands: string[] | null };

export type GatewayTool = {
  name: string;
  exposed_as: string;
  description: string;
  allowed: boolean;
  refused?: string;
  flags: string[] | null;
};

export type GatewayTools = { gateway: string; tools: GatewayTool[] | null; error: string };
