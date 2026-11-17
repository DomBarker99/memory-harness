// Everything the loop needs to know for one turn, read from two places:
//
//   /config/config.json   written by the shell at start-up from what kagent compiled
//                         (the AgentTemplate's prompt and its ModelConfig)
//   environment           the literal `env` values on the Harness, plus the
//                         placeholder keys kagent sets
//
// Nothing here is a secret. Real keys never enter the sandbox: the loop sends a
// placeholder and kagent's egress gateway swaps in the real value on the way out.
import { readFileSync } from "node:fs";

// What the gateway looks for. Any value works for it; this one says what it is.
const PLACEHOLDER_KEY = "kagent-credential-injected";

export type Config = {
  agentName: string; // the Agent's name, e.g. "memory-agent"
  instruction: string; // the AgentTemplate's systemPrompt
  model: {
    id: string; // sent to the provider as the model name
    baseUrl: string;
    api: "openai-completions" | "openai-responses"; // Pi's names for the two OpenAI wire formats
    apiKey: string;
  };
  dataDir: string; // the only folder that survives between turns
  memory: {
    url: string | undefined; // unset means memory is off and the agent runs without it
    apiKey: string;
    dataset: string; // one Cognee dataset per agent
    recallTimeoutMs: number;
    writeTimeoutMs: number;
    topK: number; // how many graph results one recall asks for
    maxRecallChars: number; // cap on what one recall adds to the prompt
  };
};

function numberFromEnv(name: string, fallback: number): number {
  const value = Number(process.env[name]);
  return Number.isFinite(value) && value > 0 ? value : fallback;
}

export function loadConfig(): Config {
  const path = process.env.HARNESS_CONFIG ?? "/config/config.json";
  const compiled = JSON.parse(readFileSync(path, "utf8"));

  // kagent writes one block per provider under agent.model. Only the OpenAI
  // shape is handled, which is what every ModelConfig on the cluster uses.
  const openai = compiled.agent?.model?.openai;
  if (!compiled.agent?.model?.model || !openai?.base_url) {
    throw new Error(`${path} has no agent.model.openai block: give the AgentTemplate an OpenAI-provider ModelConfig`);
  }

  const agentName: string = compiled.runtime?.name ?? "memory-agent";
  return {
    agentName,
    instruction: compiled.agent.instruction ?? "",
    model: {
      id: compiled.agent.model.model,
      baseUrl: openai.base_url,
      api: openai.api_format === "responses" ? "openai-responses" : "openai-completions",
      apiKey: process.env.OPENAI_API_KEY || PLACEHOLDER_KEY,
    },
    dataDir: process.env.HARNESS_DATA_DIR ?? "/data",
    memory: {
      url: process.env.COGNEE_URL?.replace(/\/+$/, "") || undefined,
      apiKey: process.env.COGNEE_API_KEY || PLACEHOLDER_KEY,
      dataset: process.env.COGNEE_DATASET || agentName,
      recallTimeoutMs: numberFromEnv("COGNEE_RECALL_TIMEOUT_MS", 8_000),
      writeTimeoutMs: numberFromEnv("COGNEE_WRITE_TIMEOUT_MS", 8_000),
      topK: numberFromEnv("COGNEE_TOP_K", 8),
      maxRecallChars: numberFromEnv("COGNEE_MAX_RECALL_CHARS", 6_000),
    },
  };
}
