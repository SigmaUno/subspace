import { execFile } from "node:child_process"

const HOOK_BIN = process.env.SUBSPACE_HOOK_BIN || "subspace-hook"
const HEARTBEAT_EVERY_MS = 30_000
const LOG_EVERY_MS = 3_000
const LOG_MAX_CHARS = 3_500
const OUTPUT_MAX_CHARS = 1_000
const INSTRUCTIONS_TTL_MS = 5 * 60_000
const REQUEST_TIMEOUT_MS = 2_000

let task = process.env.SUBSPACE_TASK_ID || ""
let lastBeat = 0
let pending = ""
let timer: ReturnType<typeof setTimeout> | undefined
let cached: { text: string; at: number } | undefined
const reported = new Set<string>()

function runHook(args: string[], input = "") {
  const child = execFile(HOOK_BIN, args, { timeout: REQUEST_TIMEOUT_MS + 1_000 }, () => {})
  child.on("error", () => {})
  child.stdin?.on("error", () => {})
  child.stdin?.end(input)
}

function withoutToken(text: string): string {
  const token = process.env.SUBSPACE_TOKEN?.trim()
  return token ? text.split(token).join("<redacted>") : text
}

function taskArgs() {
  return task ? ["--task", task] : []
}

function textOf(value: unknown): string {
  if (typeof value === "string") return value
  if (Array.isArray(value)) return value.map(textOf).join("")
  if (value && typeof value === "object") {
    const v = value as Record<string, unknown>
    return textOf(v.text ?? v.output ?? v.content)
  }
  return ""
}

function flush() {
  timer = undefined
  const chunk = pending.slice(-LOG_MAX_CHARS)
  pending = ""
  if (!chunk.trim()) return
  runHook(["log", "--stream", "stdout", ...taskArgs(), "--", "-"], chunk)
}

function observe(tool: string, args: unknown, output: unknown) {
  if (tool.startsWith("subspace_")) {
    const id = (args as { task_id?: unknown } | undefined)?.task_id
    if (tool === "subspace_claim_task" && typeof id === "string") task = id
    return
  }
  const now = Date.now()
  if (now - lastBeat >= HEARTBEAT_EVERY_MS) {
    lastBeat = now
    runHook(["heartbeat", ...taskArgs()])
  }
  const text = withoutToken(textOf(output)).trim()
  pending += `[${tool}] ${text.slice(0, OUTPUT_MAX_CHARS)}\n`
  if (!timer) {
    timer = setTimeout(flush, LOG_EVERY_MS)
    ;(timer as { unref?: () => void }).unref?.()
  }
}

function count(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? Math.round(value) : 0
}

function reportUsage(id: string, provider: unknown, model: unknown, tokens: any, cost: unknown) {
  if (!id || typeof model !== "string" || !model || reported.has(id)) return
  reported.add(id)
  if (reported.size > 1_000) reported.delete(reported.values().next().value as string)
  const usage = {
    provider: typeof provider === "string" ? provider : undefined,
    model,
    request_id: id,
    input_tokens: count(tokens?.input),
    output_tokens: count(tokens?.output) + count(tokens?.reasoning),
    cache_read_tokens: count(tokens?.cache?.read),
    cache_write_tokens: count(tokens?.cache?.write),
    cost_usd: typeof cost === "number" && cost > 0 ? cost : undefined,
  }
  runHook(["usage", ...taskArgs()], JSON.stringify(usage))
}

function reportProviderError(provider: unknown, model: unknown, status: unknown) {
  runHook(["provider-error"], JSON.stringify({ provider, model, status: count(status) || undefined }))
}

function loadInstructions(): Promise<string | undefined> {
  if (cached && Date.now() - cached.at < INSTRUCTIONS_TTL_MS) return Promise.resolve(cached.text)
  return new Promise((resolve) => {
    const child = execFile(
      HOOK_BIN,
      ["instructions"],
      { timeout: REQUEST_TIMEOUT_MS + 1_000, encoding: "utf8" },
      (err, stdout) => {
        const text = err ? "" : stdout.trim()
        if (text) cached = { text, at: Date.now() }
        resolve(cached?.text)
      },
    )
    child.on("error", () => {})
    child.stdin?.on("error", () => {})
    child.stdin?.end()
  })
}

export default {
  id: "subspace",

  async setup(ctx: any) {
    await ctx.tool.hook("execute.after", (event: any) => {
      observe(String(event.tool), event.input, event.status === "completed" ? event.result : event.error?.message)
    })
    await ctx.session.hook("context", async (event: any) => {
      const text = await loadInstructions()
      if (text) event.system.unshift({ type: "text", text })
    })
    await ctx.session.hook("retry", (event: any) => {
      reportProviderError(event.model?.providerID, event.model?.id, event.error?.status)
    })
    const models = new Map<string, { providerID?: string; id?: string }>()
    const controller = new AbortController()
    void (async () => {
      const types = ["session.step.started", "session.step.ended"]
      for await (const event of ctx.event.subscribe({ types, signal: controller.signal })) {
        const data = event.data ?? {}
        if (event.type === "session.step.started") {
          models.set(data.assistantMessageID, data.model ?? {})
          continue
        }
        const model = models.get(data.assistantMessageID)
        models.delete(data.assistantMessageID)
        if (model) reportUsage(String(event.id), model.providerID, model.id, data.tokens, data.cost)
      }
    })().catch(() => {})
    return () => controller.abort()
  },

  async server() {
    return {
      "tool.execute.after": async (input: any, output: any) => {
        observe(String(input.tool), input.args, output?.output)
      },
      event: async ({ event }: any) => {
        if (event?.type === "message.updated") {
          const info = event.properties?.info
          if (info?.role === "assistant" && info.time?.completed) {
            reportUsage(String(info.id), info.providerID, info.modelID, info.tokens, info.cost)
          }
        } else if (event?.type === "session.error" && event.properties?.error?.name === "APIError") {
          reportProviderError(undefined, undefined, event.properties.error.data?.statusCode)
        }
      },
      "experimental.chat.system.transform": async (_input: unknown, output: { system: string[] }) => {
        const text = await loadInstructions()
        if (text) output.system.unshift(text)
      },
    }
  },
}
