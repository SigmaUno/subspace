export type OnboardingState = {
  counts: { agents: number; agents_active: number };
};

export type Charter = {
  text: string;
  updated_at: string | null;
};

export type OnboardingStep = "charter" | "agents" | "brief" | "plan";

export type OnboardingProgress = Record<OnboardingStep, boolean>;

export type OnboardingMemory = "" | "active" | "dismissed";
