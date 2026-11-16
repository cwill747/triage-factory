import assert from "node:assert/strict"
import { test } from "node:test"
import { spawn } from "node:child_process"
import { allowedTool, assistantMessage, catalogStore, parseArgs, run } from "./pi-wrapper.mjs"

test("parse Go argv and reject unsupported flags", () => {
  const opts = parseArgs(["--input-format", "stream-json", "--permission-prompts", "--tf-tools", "[]", "--setting-sources", "project", "--output-format", "stream-json", "--verbose", "--allowedTools", "Read,Bash(git status)"])
  assert.equal(opts.interactive, true)
  assert.equal(opts.permissions, true)
  assert.deepEqual(opts.allowed, ["Read", "Bash(git status)"])
  assert.throws(() => parseArgs(["--claude-bin", "/bin/claude"]))
  assert.throws(() => parseArgs(["--tf-tools", "not json"]))
})

test("tool grants fail closed for unlisted and compound shell commands", () => {
  const rules = ["Read", "Edit", "Bash(git status)", "Bash(go test *)"]
  assert.equal(allowedTool("read", {}, rules), true)
  assert.equal(allowedTool("write", {}, rules), false)
  assert.equal(allowedTool("bash", { command: "git status" }, rules), true)
  assert.equal(allowedTool("bash", { command: "go test ./..." }, rules), true)
  for (const command of ["git push", "git status; rm -rf /", "go test $(evil)", "go test ./...\nevil", "go test ./... | evil", "go test `evil`", "go test > file"]) {
    assert.equal(allowedTool("bash", { command }, rules), false, command)
  }
})

test("Pi assistant content and usage map to the existing parser", () => {
  const message = assistantMessage({ model: "test", stopReason: "toolUse", content: [{ type: "thinking", thinking: "reason" }, { type: "text", text: "hello" }, { type: "toolCall", id: "c1", name: "bash", arguments: { command: "pwd" } }], usage: { input: 4, output: 3, cacheRead: 2, cacheWrite: 1 } })
  assert.equal(message.stop_reason, "tool_use")
  assert.deepEqual(message.content[2], { type: "tool_use", id: "c1", name: "Bash", input: { command: "pwd" } })
  assert.equal(message.usage.cache_read_input_tokens, 2)
})

// Exercise the wrapper without provider credentials or model requests.
function fakeSDK({ failure = false, withTool = false } = {}) {
  let listener
  let configured
  const message = { role: "assistant", model: "fake", stopReason: failure ? "error" : "stop", errorMessage: failure ? "provider failed" : undefined, content: [{ type: "text", text: "answer" }], usage: { cost: { total: 0.25 } } }
  const session = {
    agent: {},
    subscribe(fn) { listener = fn },
    async bindExtensions() {},
    async prompt() {
      if (withTool) {
        const tool = configured.customTools[0]
        await assert.rejects(tool.execute("denied", { command: "evil" }), /not allowed/)
        await tool.execute("allowed", { command: "pwd" })
        listener({ type: "message_end", message: { role: "toolResult", toolCallId: "allowed", content: [{ type: "text", text: "ok" }] } })
        await session.agent.finishTurn({ toolResults: [{ toolCallId: "allowed" }] })
      }
      listener({ type: "message_end", message })
    },
    getLastAssistantText() { return "answer" },
    async abort() {},
    async steer(text) { assert.equal(text, "batch context") },
    dispose() {},
  }
  const manager = { getSessionFile: () => undefined, getSessionId: () => "fake" }
  return {
    createCodingTools: () => [{ name: "bash", description: "shell", parameters: { type: "object" }, execute: async () => ({ content: [{ type: "text", text: "ok" }] }) }],
    SettingsManager: { inMemory: () => ({}) },
    DefaultResourceLoader: class {
      constructor(opts) { assert.equal(opts.noExtensions, true); assert.equal(opts.noContextFiles, true) }
      async reload() {}
    },
    getAgentDir: () => "/nonexistent-tf-test-pi-config",
    ModelRuntime: { create: async (opts) => {
      assert.equal(opts.modelsPath, "/nonexistent-tf-test-pi-config/models.json")
      return { getAllModels: () => [], async settleCredentialWrites() {} }
    } },
    SessionManager: { inMemory: () => manager, create: () => manager },
    createAgentSession: async (opts) => { configured = opts; return { session } },
  }
}

async function capture(fn) {
  const lines = []
  const original = process.stdout.write
  process.stdout.write = (line) => { lines.push(JSON.parse(line)); return true }
  try { await fn() } finally { process.stdout.write = original }
  return lines
}

test("one-shot emits init, transcript, usage and success", async () => {
  const lines = await capture(() => run(parseArgs(["-p", "hello", "--allowedTools", "Bash(pwd)"]), fakeSDK({ withTool: true })))
  assert.equal(lines[0].session_id, "pi:fake")
  assert.equal(lines.find((line) => line.subtype === "tool_started").tool_call_id, "allowed")
  const result = lines.at(-1)
  assert.equal(result.type, "result")
  assert.equal(result.is_error, false)
  assert.equal(result.total_cost_usd, 0.25)
  assert.equal(result.result, "answer")
})

test("catalog store tolerates missing cache and keeps writes in memory", async () => {
  const store = catalogStore("/nonexistent-tf-test-pi-config/models-store.json")
  assert.equal(await store.read("radius"), undefined)
  const entry = { models: [{ id: "example" }] }
  await store.write("radius", entry)
  entry.models.length = 0
  assert.equal((await store.read("radius")).models.length, 1)
  await store.delete("radius")
  assert.equal(await store.read("radius"), undefined)
})

test("provider failure becomes an error result", async () => {
  const lines = await capture(() => run(parseArgs(["-p", "hello"]), fakeSDK({ failure: true })))
  assert.equal(lines.at(-1).is_error, true)
  assert.equal(lines.at(-1).result, "provider failed")
})

test("interactive protocol handles permissions, batch context and follow-ups", { timeout: 10000 }, async () => {
  const script = `import assert from "node:assert/strict";
    import { run, parseArgs } from ${JSON.stringify(new URL("./pi-wrapper.mjs", import.meta.url).href)};
    ${fakeSDK.toString()}
    await run(parseArgs(["--input-format", "stream-json", "--permission-prompts", "--tool-batch-notes", "--allowedTools", "Bash(pwd)"]), fakeSDK({ withTool: true }));`
  const child = spawn(process.execPath, ["--input-type=module", "-e", script], { stdio: ["pipe", "pipe", "pipe"] })
  const lines = []
  let buffer = ""
  let stderr = ""
  let results = 0
  const send = (obj) => child.stdin.write(JSON.stringify(obj) + "\n")
  child.stderr.on("data", (data) => { stderr += data })
  child.stdout.on("data", (data) => {
    buffer += data
    let end
    while ((end = buffer.indexOf("\n")) >= 0) {
      const line = JSON.parse(buffer.slice(0, end))
      buffer = buffer.slice(end + 1)
      lines.push(line)
      if (line.subtype === "ready") send({ kind: "user_message", text: "first" })
      if (line.subtype === "permission_request") send({ kind: "permission_response", tool_call_id: line.tool_call_id, behavior: "deny" })
      if (line.subtype === "tool_batch") send({ kind: "tool_batch_reply", id: line.id, context: "batch context" })
      if (line.type === "result") {
        results++
        send(results === 1 ? { kind: "user_message", text: "follow-up" } : { kind: "end" })
      }
    }
  })
  try {
    const code = await new Promise((resolve, reject) => { child.on("error", reject); child.on("close", resolve) })
    assert.equal(code, 0, stderr)
    assert.equal(stderr, "")
    assert.equal(results, 2)
    assert.equal(lines.filter((line) => line.subtype === "tool_batch_result" && line.delivered).length, 2)
    assert.equal(lines.filter((line) => line.subtype === "permission_request").length, 2)
  } finally { child.kill() }
})
