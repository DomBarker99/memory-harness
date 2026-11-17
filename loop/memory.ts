// The Cognee client: the three calls the loop makes around every turn.
//
//   recall    before the model runs: what does long-term memory hold about this message?
//   capture   after it: store the question, the answer and the tool calls
//   improve   then: ask Cognee to fold this Session's entries into the agent's graph
//
// Cognee runs outside the sandbox as its own service. This file only speaks HTTP
// to it. The store is one dataset per agent, and the API key decides which agent
// is calling, so one agent cannot read another's memory.
//
// Every call here has a timeout and none of them throws. Memory failing must
// never fail the turn: a harness that dies mid-turn leaves its sandbox stuck.
import type { Config } from "./config.ts";

export type ToolTrace = {
  name: string;
  args: unknown;
  result: string;
  isError: boolean;
};

export type Memory = {
  enabled: boolean;
  recall(query: string): Promise<string>; // "" when there is nothing, or when Cognee could not be asked
  capture(turn: { sessionId: string; question: string; answer: string; tools: ToolTrace[] }): Promise<boolean>; // false if a write failed
  improve(sessionId: string): Promise<void>;
};

// One line on stderr per memory call, as JSON so the worker log keeps the fields.
function log(fields: Record<string, unknown>): void {
  console.error(JSON.stringify({ at: "memory", ...fields }));
}

// Tool arguments and output can be huge. Memory gets the start of each.
const TRACE_LIMIT = 2_000;
function clip(value: unknown): string {
  const text = typeof value === "string" ? value : JSON.stringify(value) ?? "";
  return text.length > TRACE_LIMIT ? text.slice(0, TRACE_LIMIT) + " [cut]" : text;
}

// Asked for context only, Cognee still wraps what it found in the prompt it
// would have given its own model ("The question is: ... Context: `...`").
// Only the part between the backticks is memory; the rest is instructions meant
// for a different model, and must not reach ours.
function unwrapContext(text: string): string {
  const match = /\nContext:\n`([\s\S]*)`\s*$/.exec(text);
  return (match ? match[1] : text).trim();
}

// The context also lists Cognee's own bookkeeping as if it were knowledge
// ("x belongs to set user_sessions_from_cache", "y is part of text_3bb0...").
// Those lines say nothing about the world, so they are dropped.
const BOOKKEEPING = [/ belongs to set \S+\.?$/, / is part of text_[0-9a-f]+\.?$/, /^- Document chunk mentions /];
function dropBookkeeping(text: string): string {
  return text
    .split("\n")
    .filter((line) => !BOOKKEEPING.some((pattern) => pattern.test(line)))
    .join("\n");
}

export function createMemory(config: Config["memory"]): Memory {
  const { url } = config;
  if (!url) {
    log({ event: "off", reason: "COGNEE_URL is not set" });
    return { enabled: false, recall: async () => "", capture: async () => false, improve: async () => {} };
  }

  // POST one JSON body. Returns the parsed reply, or undefined on any failure.
  async function post(path: string, body: unknown, timeoutMs: number): Promise<unknown> {
    const started = Date.now();
    try {
      const response = await fetch(url + path, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Api-Key": config.apiKey },
        body: JSON.stringify(body),
        signal: AbortSignal.timeout(timeoutMs),
      });
      const text = await response.text();
      const ms = Date.now() - started;
      if (!response.ok) {
        // A dataset that has never been written to does not exist yet. That is
        // an agent with nothing remembered, not a fault.
        const empty = text.includes("DatasetNotFoundError");
        log({ event: empty ? "empty" : "error", path, status: response.status, ms, detail: empty ? undefined : text.slice(0, 300) });
        return undefined;
      }
      log({ event: "ok", path, ms });
      return JSON.parse(text);
    } catch (error) {
      log({ event: "error", path, ms: Date.now() - started, detail: String(error) });
      return undefined;
    }
  }

  return {
    enabled: true,

    // Searches the agent's graph, which holds what earlier Sessions learned.
    // No session id is sent: this Session's own turns are already in the
    // conversation, so only the long-term side is asked.
    async recall(query) {
      const results = await post(
        "/api/v1/recall",
        { query, datasets: [config.dataset], onlyContext: true, topK: config.topK },
        config.recallTimeoutMs,
      );
      if (!Array.isArray(results)) return "";
      const parts = results
        .map((entry) => (typeof entry?.text === "string" ? dropBookkeeping(unwrapContext(entry.text)) : ""))
        .filter((text) => text !== "");
      const joined = parts.join("\n\n");
      return joined.length > config.maxRecallChars ? joined.slice(0, config.maxRecallChars) + "\n[cut]" : joined;
    },

    // Writes land in Cognee's session store, which is fast and builds nothing.
    // Tool calls go first so the answer is the newest entry. The first write
    // that fails ends the capture: an unreachable Cognee then costs one
    // timeout, not one per tool call.
    async capture({ sessionId, question, answer, tools }) {
      const target = { dataset_name: config.dataset, session_id: sessionId };
      for (const tool of tools) {
        const written = await post(
          "/api/v1/remember/entry",
          {
            ...target,
            entry: {
              type: "trace",
              origin_function: tool.name,
              status: tool.isError ? "error" : "success",
              method_params: { args: clip(tool.args) },
              method_return_value: clip(tool.result),
              memory_query: question.slice(0, TRACE_LIMIT),
            },
          },
          config.writeTimeoutMs,
        );
        if (written === undefined) return false;
      }
      const written = await post("/api/v1/remember/entry", { ...target, entry: { type: "qa", question, answer } }, config.writeTimeoutMs);
      return written !== undefined;
    },

    // Starts the graph build and returns at once. The work happens in Cognee's
    // pod, so it carries on after this sandbox is suspended.
    async improve(sessionId) {
      await post(
        "/api/v1/improve",
        { datasetName: config.dataset, sessionIds: [sessionId], runInBackground: true },
        config.writeTimeoutMs,
      );
    },
  };
}
