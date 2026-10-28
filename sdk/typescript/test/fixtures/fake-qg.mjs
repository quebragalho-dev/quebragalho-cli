#!/usr/bin/env node
// Stand-in for `qg exec -o stream-json`: behaviour chosen by FAKE_MODE.
import { createInterface } from "node:readline";

const mode = process.env.FAKE_MODE ?? "ok";
const ev = (e) => process.stdout.write(JSON.stringify({ schemaVersion: 2, runId: "run_1", ...e }) + "\n");

if (mode === "echo-args") {
  const lines = createInterface({ input: process.stdin });
  let prompt = "";
  for await (const l of lines) prompt += l;
  ev({ type: "run_start", sessionId: "s1", model: "m" });
  ev({ type: "final", text: JSON.stringify({ argv: process.argv.slice(2), prompt: JSON.parse(prompt), key: process.env.QG_API_KEY ?? null }) });
  ev({ type: "run_end", status: "success", exitCode: 0 });
} else if (mode === "ok") {
  process.stdin.resume();
  ev({ type: "run_start", sessionId: "s1", model: "m", provider: "p" });
  ev({ type: "text", delta: "hel" });
  ev({ type: "text", delta: "lo" });
  ev({ type: "tool_result", id: "c1", status: "ok", changedFiles: ["a.txt", "a.txt", "b.txt"] });
  ev({ type: "usage", promptTokens: 10, completionTokens: 2, totalTokens: 12, costUsd: 0.5 });
  ev({ type: "warning", message: "careful" });
  ev({ type: "final", text: "hello" });
  ev({ type: "run_end", status: "success", exitCode: 0 });
} else if (mode === "provider-error") {
  process.stdin.resume();
  ev({ type: "run_start" });
  ev({ type: "error", code: "provider_error", message: "boom", recoverable: false });
  ev({ type: "run_end", status: "error", exitCode: 3 });
  process.exitCode = 3;
} else if (mode === "no-run-end") {
  process.stdin.resume();
  ev({ type: "run_start" });
  process.stderr.write("crashed\n");
  process.exit(1);
} else if (mode === "garbage") {
  process.stdout.write("not json\n");
} else if (mode === "future-schema") {
  process.stdout.write(JSON.stringify({ schemaVersion: 3, type: "run_start", runId: "r" }) + "\n");
} else if (mode === "hang") {
  process.stdin.resume();
  ev({ type: "run_start" });
  setInterval(() => {}, 1000);
}
