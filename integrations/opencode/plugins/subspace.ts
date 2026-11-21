import { execFile } from "node:child_process"
import { readFileSync } from "node:fs"

const HOOK_BIN = process.env.SUBSPACE_HOOK_BIN || "subspace-hook"
const HEARTBEAT_EVERY_MS = 30_000
const LOG_EVERY_MS = 3_000
const LOG_MAX_CHARS = 4_000
const OUTPUT_MAX_CHARS = 1_000
const INSTRUCTIONS_TTL_MS = 5 * 60_000
const REQUEST_TIMEOUT_MS = 2_000

let task = process.env.SUBSPACE_TASK_ID || ""
let lastBeat = 0
let pending = ""
let timer: ReturnType<typeof setTimeout> | undefined
let seq = Date.now()
let cached: { text: string; at: number } | undefined

function runHook(args: string[], stdin?: string) {
  const child = execFile(HOOK_BIN, args, { timeout: REQUEST_TIMEOUT_MS + 1_000 }, () => {})
  child.on("error", () => {})
  child.stdin?.on("error", () => {})
  child.stdin?.end(stdin ?? "")
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
  runHook(["log", "--stream", "stdout", "--seq", String(++seq), ...taskArgs()], chunk)
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

function readToken() {
  const token = (process.env.SUBSPACE_TOKEN || "").trim()
  if (token) return token
  const file = process.env.SUBSPACE_TOKEN_FILE
  return file ? readFileSync(file, "utf8").trim() : ""
}

async function loadInstructions() {
  if (cached && Date.now() - cached.at < INSTRUCTIONS_TTL_MS) return cached.text
  try {
    const base = (process.env.SUBSPACE_URL || "").replace(/\/+$/, "")
    const token = readToken()
    if (!base || !token) return cached?.text
    const res = await fetch(`${base}/mcp`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
        Accept: "application/json, text/event-stream",
      },
      body: JSON.stringify({
        jsonrpc: "2.0",
        id: 1,
        method: "tools/call",
        params: { name: "get_instructions", arguments: {} },
      }),
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    })
    if (!res.ok) return cached?.text
    const body = (await res.json()) as { result?: { structuredContent?: { instructions?: string } } }
    const text = body.result?.structuredContent?.instructions
    if (text) cached = { text, at: Date.now() }
    return cached?.text
  } catch {
    return cached?.text
  }
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
