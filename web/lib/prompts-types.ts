export type Prompt = {
  id: string;
  title: string;
  tags: string[];
  variables: string[];
  body: string;
};

export type PromptList = { prompts: Prompt[] };

export type RenderedPrompt = { id: string; text: string };
