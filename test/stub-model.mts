// A stand-in for the model, so the harness can be run without spending inference.
//
// It speaks just enough of the OpenAI chat-completions API (streaming) for Pi,
// and follows a fixed script instead of thinking:
//
//   user message starts with "run: <command>"     -> calls the bash tool with that command
//   user message starts with "recall: <query>"    -> calls the recall tool with that query
//   the last message is a tool result             -> replies "Tool said: <result>"
//   anything else                                 -> replies "Stub reply to: <message>"
//
// "User message" means the newest one the person wrote. The note the loop adds
// with recalled memory also arrives with the user role, and is skipped here.
//
// Every request it receives is appended to a log file as one JSON line, so a
// test can check what the model was actually sent (the system prompt, the
// recalled memory, the Authorization header).
//
//   node test/stub-model.mts [port] [log-file]
import { appendFileSync } from "node:fs";
import { createServer } from "node:http";

const port = Number(process.argv[2] ?? 18317);
const logFile = process.argv[3] ?? "/dev/null";

type Message = { role: string; content?: unknown; tool_calls?: unknown };

// Message content is a string, or a list of parts with text in them.
function textOf(content: unknown): string {
  if (typeof content === "string") return content;
  if (Array.isArray(content)) return content.map((part) => part?.text ?? "").join("");
  return "";
}

function decide(messages: Message[]): { text: string } | { tool: string; args: object } {
  const last = messages.at(-1);
  if (last?.role === "tool") return { text: `Tool said: ${textOf(last.content).trim().slice(0, 400)}` };
  const written = messages.filter((message) => message.role === "user" && !textOf(message.content).startsWith("Recalled from long-term memory"));
  const user = textOf(written.at(-1)?.content).trim();
  if (user.startsWith("run:")) return { tool: "bash", args: { command: user.slice(4).trim() } };
  if (user.startsWith("recall:")) return { tool: "recall", args: { query: user.slice(7).trim() } };
  return { text: `Stub reply to: ${user}` };
}

createServer((request, response) => {
  let body = "";
  request.on("data", (chunk) => (body += chunk));
  request.on("end", () => {
    if (request.method !== "POST" || !request.url?.endsWith("/chat/completions")) {
      response.writeHead(404).end();
      return;
    }
    const payload = JSON.parse(body);
    appendFileSync(
      logFile,
      JSON.stringify({
        authorization: request.headers.authorization,
        model: payload.model,
        tools: (payload.tools ?? []).map((tool: { function?: { name?: string } }) => tool.function?.name),
        messages: payload.messages,
      }) + "\n",
    );

    const base = { id: "stub", object: "chat.completion.chunk", created: 0, model: payload.model };
    const send = (choice: object) => response.write(`data: ${JSON.stringify({ ...base, choices: [{ index: 0, ...choice }] })}\n\n`);
    response.writeHead(200, { "Content-Type": "text/event-stream" });

    const decision = decide(payload.messages);
    if ("text" in decision) {
      // In a few pieces, so streaming is exercised.
      const words = decision.text.split(/(?<= )/);
      send({ delta: { role: "assistant", content: "" }, finish_reason: null });
      for (const word of words) send({ delta: { content: word }, finish_reason: null });
      send({ delta: {}, finish_reason: "stop" });
    } else {
      send({
        delta: {
          role: "assistant",
          tool_calls: [
            { index: 0, id: `call_${Date.now()}`, type: "function", function: { name: decision.tool, arguments: JSON.stringify(decision.args) } },
          ],
        },
        finish_reason: null,
      });
      send({ delta: {}, finish_reason: "tool_calls" });
    }
    response.write(
      `data: ${JSON.stringify({ ...base, choices: [], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } })}\n\n`,
    );
    response.end("data: [DONE]\n\n");
  });
}).listen(port, "127.0.0.1", () => console.error(`stub model on http://127.0.0.1:${port}/v1, logging to ${logFile}`));
