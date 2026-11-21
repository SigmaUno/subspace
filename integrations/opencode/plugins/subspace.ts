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

function runHook(args: string[]) {
  const child = execFile(HOOK_BIN, args, { timeout: REQUEST_TIMEOUT_MS + 1_000 }, () => {})
  child.on("error", () => {})
  child.stdin?.on("error", () => {})
  child.stdin?.end()
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
  runHook(["log", "--stream", "stdout", ...taskArgs(), "--", chunk])
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
  const text = textOf(output).trim()
  pending += `[${tool}] ${text.slice(0, OUTPUT_MAX_CHARS)}\n`
  if (!timer) {
    timer = setTimeout(flush, LOG_EVERY_MS)
    ;(timer as { unref?: () => void }).unref?.()
  }
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
  },

  async server() {
    return {
      "tool.execute.after": async (input: any, output: any) => {
        observe(String(input.tool), input.args, output?.output)
      },
      "experimental.chat.system.transform": async (_input: unknown, output: { system: string[] }) => {
        const text = await loadInstructions()
        if (text) output.system.unshift(text)
      },
    }
  },
}
