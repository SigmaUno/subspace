type BriefSpec = {
  template_id: string;
  markdown: string;
  vars: Record<string, string>;
};

type TaskSpec = {
  template_id: string;
  vars: Record<string, string>;
  repo: string;
  priority: number;
};

export type Schedule = {
  id: string;
  name: string;
  cron: string;
  timezone: string;
  action: "create_brief" | "add_tasks";
  brief: BriefSpec | null;
  brief_id: string;
  tasks: TaskSpec[];
  enabled: boolean;
  max_active: number;
  catch_up: "skip" | "run-once";
  next_run_at: string | null;
  last_run_at: string | null;
  created_at: string;
  updated_at: string;
};

export type ScheduleList = { schedules: Schedule[] | null };

export type Run = {
  schedule_id: string;
  scheduled_for: string;
  status: "running" | "created" | "skipped" | "failed" | "interrupted";
  missed: number;
  detail: string;
  brief_id: string;
  task_ids: string[];
  started_at: string;
  finished_at: string | null;
};

export type RunList = { runs: Run[] | null };

export type Preview = { cron: string; timezone: string; next: string[] };

export type TemplateSummary = { id: string; title: string; variables: string[] };

export type TemplateList = { templates: TemplateSummary[] | null };
