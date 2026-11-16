// Adapt Pi's SDK to the existing agentproc NDJSON and live-control protocol.
import { randomUUID } from "node:crypto"
import { createInterface } from "node:readline"
import { basename, join } from "node:path"
import { existsSync, readFileSync } from "node:fs"
import { pathToFileURL } from "node:url"

export function parseArgs(argv) {
  const options = { allowed: [], tfTools: [] }
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i]
    const next = () => argv[++i]
    switch (flag) {
      case "-p": options.prompt = next(); break
      case "--resume": options.resume = next(); break
      case "--model": options.model = next(); break
      case "--permission-mode": options.mode = next(); break
      case "--allowedTools": options.allowed = next().split(","); break
      case "--append-system-prompt": options.systemPrompt = next(); break
      case "--max-turns": options.maxTurns = Number(next()); break
      case "--input-format": options.interactive = next() === "stream-json"; break
      case "--permission-prompts": options.permissions = true; break
      case "--tool-batch-notes": options.batchNotes = true; break
      case "--tf-tools":
        try { options.tfTools = JSON.parse(next()) }
        catch (err) { throw new Error(`Invalid TF tools JSON: ${err}`) }
        break
      case "--setting-sources":
      case "--output-format":
      case "--disallowedTools":
      case "--add-dir": next(); break
      case "--verbose": break
      default: throw new Error(`Unsupported Pi wrapper flag: ${flag}`)
    }
  }
  return options
}

const names = { read: "Read", edit: "Edit", write: "Write", bash: "Bash" }
export function allowedTool(name, input, rules) {
  const mapped = names[name] ?? name
  if (rules.includes(mapped)) return true
  // Claude's shell parser does not exist here. Compound commands always need
  // approval rather than treating a glob match as permission for the whole shell.
  if (name !== "bash" || /[\n\r;&|`$<>()\\]/.test(input.command ?? "")) return false
  return rules.some((rule) => {
    if (!rule.startsWith("Bash(") || !rule.endsWith(")")) return false
    const pattern = rule.slice(5, -1).split("*").map((part) => part.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join(".*")
    return new RegExp(`^${pattern}$`).test(input.command)
  })
}

export function assistantMessage(message) {
  return {
    id: randomUUID(), role: "assistant", model: message.model,
    content: (message.content ?? []).map((block) => {
      if (block.type === "toolCall") return { type: "tool_use", id: block.id, name: names[block.name] ?? block.name, input: block.arguments }
      return block
    }),
    stop_reason: ({ stop: "end_turn", toolUse: "tool_use", length: "max_tokens" })[message.stopReason] ?? message.stopReason,
    usage: {
      input_tokens: message.usage?.input ?? 0,
      output_tokens: message.usage?.output ?? 0,
      cache_read_input_tokens: message.usage?.cacheRead ?? 0,
      cache_creation_input_tokens: message.usage?.cacheWrite ?? 0,
    },
  }
}

// Keep refreshed provider catalogs in memory. The host cache is read-only in
// the sandbox; credentials alone need write-through for rotating OAuth tokens.
export function catalogStore(path) {
  let entries = {}
  try { entries = JSON.parse(readFileSync(path, "utf8")) }
  catch (err) { if (err.code !== "ENOENT") throw err }
  return {
    async read(id) { return structuredClone(entries[id]) },
    async write(id, entry) { entries[id] = structuredClone(entry) },
    async delete(id) { delete entries[id] },
  }
}

export async function run(options, sdk) {
  const emit = (obj) => process.stdout.write(JSON.stringify(obj) + "\n")
  const cwd = process.cwd()
  const pending = new Map()
  let closing = false
  let session
  let mode = options.mode ?? "default"
  let cost = 0
  let turns = 0
  let turnCount = 0
  let last
  const started = Date.now()
  const request = (key, event, signal) => new Promise((resolve) => {
    const settle = (reply) => { pending.delete(key); resolve(reply) }
    pending.set(key, settle)
    if (signal?.aborted || closing) return settle({})
    signal?.addEventListener("abort", () => settle({}), { once: true })
    emit(event)
  })
  const settlePending = () => {
    for (const settle of [...pending.values()]) settle({})
  }
  const batch = async (ids, signal) => {
    if (!options.batchNotes && !options.tfTools.length) return {}
    const id = randomUUID()
    const reply = await request(id, { type: "control", subtype: "tool_batch", id, tool_call_ids: ids }, signal)
    if (reply.context) {
      await session.steer(reply.context)
      emit({ type: "control", subtype: "tool_batch_result", id, delivered: true })
    }
    return reply.stop ? { action: "end" } : undefined
  }
  const tools = sdk.createCodingTools(cwd).map((tool) => ({
    ...tool, label: names[tool.name] ?? tool.name,
    execute: async (id, input, signal, update) => {
      let approved = mode === "bypassPermissions" || allowedTool(tool.name, input, options.allowed)
      if (!approved && options.permissions) {
        const reply = await request(id, { type: "control", subtype: "permission_request", tool_call_id: id, tool_name: names[tool.name] ?? tool.name, input }, signal)
        approved = reply.behavior === "allow"
        if (reply.updated_input) input = reply.updated_input
      }
      if (!approved) throw new Error("Tool is not allowed by Triage Factory")
      emit({ type: "control", subtype: "tool_started", tool_call_id: id })
      return tool.execute(id, input, signal, update)
    },
  }))
  for (const def of options.tfTools) tools.push({
    name: `mcp__tf__${def.name}`, label: def.name, description: def.description, parameters: def.input_schema,
    execute: async (toolCallID, input, signal) => {
      const id = randomUUID()
      const reply = await request(id, { type: "control", subtype: "tool_call", id, tool_call_id: toolCallID, tool_name: def.name, input }, signal)
      if (signal?.aborted) emit({ type: "control", subtype: "tool_call_cancel", id })
      if (reply.is_error) throw new Error(reply.content ?? "TF tool failed")
      return { content: [{ type: "text", text: reply.content ?? "" }], details: {} }
    },
  })
  const settingsManager = sdk.SettingsManager.inMemory({ compaction: { enabled: true }, retry: { enabled: true } })
  // No operator or repository executable extensions/settings are loaded. Pi's
  // provider auth/models remain available through ModelRuntime alone.
  const resourceLoader = new sdk.DefaultResourceLoader({
    cwd, agentDir: join(cwd, "_tfac", "pi"), settingsManager,
    noExtensions: true, noSkills: true, noPromptTemplates: true, noThemes: true, noContextFiles: true,
    appendSystemPrompt: options.systemPrompt ? [options.systemPrompt] : [],

  })
  await resourceLoader.reload()
  const modelName = process.env.TF_PI_MODEL || options.model
  const agentDir = sdk.getAgentDir()
  const modelRuntime = await sdk.ModelRuntime.create({
    authPath: join(agentDir, "auth.json"), modelsPath: join(agentDir, "models.json"),
    modelsStore: catalogStore(join(agentDir, "models-store.json")),
    allowModelNetwork: modelName?.startsWith("radius/") ?? false,
    modelRefreshTimeoutMs: 10000,
  })
  let model
  if (modelName) {
    const models = modelRuntime.getAllModels()
    model = models.find((m) => `${m.provider}/${m.id}` === modelName) ?? models.find((m) => m.id === modelName)
    if (!model) throw new Error(`Pi model not found: ${modelName}; set TF_PI_MODEL=provider/model-id`)
  }
  if (options.resume && (!options.resume.startsWith("pi:") || !/^[\w.-]+\.jsonl$/.test(options.resume.slice(3)))) throw new Error("Cannot resume an invalid or Claude session with Pi")
  const resumePath = options.resume && join(cwd, "_tfac", "pi", "sessions", options.resume.slice(3))
  if (resumePath && !existsSync(resumePath)) throw new Error(`Pi session is missing: ${resumePath}`)
  let sessionManager
  if (options.resume) sessionManager = sdk.SessionManager.open(resumePath, undefined, cwd)
  else if (options.interactive) sessionManager = sdk.SessionManager.create(cwd, join(cwd, "_tfac", "pi", "sessions"))
  else sessionManager = sdk.SessionManager.inMemory(cwd)
  ;({ session } = await sdk.createAgentSession({ cwd, model, modelRuntime, settingsManager, resourceLoader, sessionManager, tools: [], customTools: tools }))
  const sessionID = `pi:${basename(sessionManager.getSessionFile() ?? sessionManager.getSessionId())}`
  emit({ type: "system", subtype: "init", session_id: sessionID })
  session.subscribe((event) => {
    if (event.type !== "message_end") return
    const msg = event.message
    if (msg.role === "assistant") {
      last = msg
      turns++
      turnCount++
      cost += msg.usage?.cost?.total ?? 0
      emit({ type: "assistant", message: assistantMessage(msg) })
    } else if (msg.role === "toolResult") {
      emit({ type: "user", message: { role: "user", content: [{ type: "tool_result", tool_use_id: msg.toolCallId, content: msg.content, is_error: msg.isError }] } })
    }
  })
  await session.bindExtensions({})
  // The agent scheduling hook can actually end a tool loop; a turn_end
  // extension's continue:false only declines to request an extra turn.
  const finishTurn = session.agent.finishTurn
  session.agent.finishTurn = async (turn, signal) => {
    const previous = await finishTurn?.(turn, signal)
    if (previous?.action === "end") return previous
    if (turn.toolResults.length) {
      const decision = await batch(turn.toolResults.map((result) => result.toolCallId), signal)
      if (decision?.action === "end" || (options.maxTurns && turnCount >= options.maxTurns)) return { action: "end" }
    }
    return previous
  }
  const prompt = async (text) => {
    last = undefined
    turnCount = 0
    try {
      await session.prompt(text, { expandPromptTemplates: false })
      const failed = !last || ["error", "aborted", "length"].includes(last.stopReason)
      emit({ type: "result", subtype: failed ? "error_during_execution" : "success", is_error: failed,
        result: last?.errorMessage ?? session.getLastAssistantText() ?? "", total_cost_usd: cost,
        duration_ms: Date.now() - started, num_turns: turns, session_id: sessionID,
        stop_reason: ({ stop: "end_turn", toolUse: "tool_use", length: "max_tokens" })[last?.stopReason] ?? last?.stopReason })
    } catch (err) {
      emit({ type: "result", subtype: "error_during_execution", is_error: true, result: String(err), total_cost_usd: cost, duration_ms: Date.now() - started, num_turns: turns })
    }
  }
  if (!options.interactive) {
    try { await prompt(options.prompt) }
    finally { session.dispose(); await modelRuntime.settleCredentialWrites() }
    return
  }
  let prompts = Promise.resolve()
  const rl = createInterface({ input: process.stdin })
  const close = async () => {
    if (closing) return
    closing = true
    settlePending()
    await session.abort()
    session.dispose()
    await modelRuntime.settleCredentialWrites()
    rl.close()
  }
  rl.on("close", () => { void close() })
  rl.on("line", (line) => {
    try {
      const ctl = JSON.parse(line)
      if (ctl.kind === "permission_response") pending.get(ctl.tool_call_id)?.(ctl)
      else if (ctl.kind === "tool_call_reply" || ctl.kind === "tool_batch_reply") {
        const settle = pending.get(ctl.id)
        if (settle) settle(ctl)
        else if (ctl.kind === "tool_batch_reply" && ctl.context) emit({ type: "control", subtype: "tool_batch_result", id: ctl.id, delivered: false })
      }
      else if (ctl.kind === "user_message") {
        const text = ctl.blocks ? ctl.blocks.map((b) => b.text).join("\n\n") : ctl.text
        prompts = prompts.then(() => closing ? undefined : prompt(text))
      } else if (ctl.kind === "interrupt") {
        settlePending()
        void session.abort().then(() => emit({ type: "control", subtype: "interrupted" }))
      } else if (ctl.kind === "set_mode") mode = ctl.mode
      else if (ctl.kind === "end") void close()
      else throw new Error(`Unknown Pi control: ${ctl.kind}`)
    } catch (err) { process.stderr.write(`pi-wrapper: ${err}\n`) }
  })
  emit({ type: "control", subtype: "ready" })
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const sdk = await import(pathToFileURL(join(process.env.TF_PI_SDK_PATH, "dist", "index.js")).href)
    await run(parseArgs(process.argv.slice(2)), sdk)
  } catch (err) {
    process.stderr.write(`pi-wrapper: ${err?.stack ?? err}\n`)
    process.exitCode = 1
  }
}
