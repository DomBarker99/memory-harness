// The one memory tool the model can call itself.
//
// Every turn already starts with an automatic recall on the user's message
// (loop.ts). This tool is for when the model needs to ask memory a different
// question part-way through its work. Writing to memory is not a tool: the loop
// captures every turn on its own.
import type { ToolDefinition } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";
import type { Memory } from "./memory.ts";

export function recallTool(memory: Memory): ToolDefinition {
  return {
    name: "recall",
    label: "Recall",
    description:
      "Search this agent's long-term memory: facts, decisions and results from earlier conversations. " +
      "Returns the passages and related facts that match, or says that nothing was found.",
    promptSnippet: "Search long-term memory from earlier conversations",
    promptGuidelines: [
      "Memory from earlier conversations is looked up automatically for each user message. Call recall when you need to search it for something else.",
      "Treat what recall returns as notes from the past: check anything that may have changed before relying on it.",
    ],
    parameters: Type.Object({
      query: Type.String({ description: "What to look for, as a question or a few key words" }),
    }),
    async execute(_toolCallId, params) {
      const { query } = params as { query: string };
      const found = await memory.recall(query);
      return {
        content: [{ type: "text", text: found || "Nothing in memory matched." }],
        details: { query, found: found !== "" },
      };
    },
  };
}
