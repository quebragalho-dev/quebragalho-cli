import { spawn } from "node:child_process";
import { buildArgs } from "./args.ts";
import { Run } from "./run.ts";
import type { PromptInput, QGOptions, RunOptions, RunResult } from "./types.ts";
import { SCHEMA_VERSION } from "./types.ts";

export class QG {
  readonly #options: QGOptions;

  constructor(options: QGOptions = {}) {
    this.#options = options;
  }

  /** Start a run and return a live handle (async-iterable events + result()). */
  stream(prompt: string | PromptInput, options: RunOptions = {}): Run {
    const merged: RunOptions = { ...this.#options.defaults, ...options };
    merged.cwd ??= this.#options.cwd;
    const binary = this.#options.binary ?? process.env.QG_BIN ?? "qg";
    const env: NodeJS.ProcessEnv = { ...process.env, ...this.#options.env };
    if (this.#options.apiKey) env.QG_API_KEY = this.#options.apiKey;

    const child = spawn(binary, buildArgs(merged), {
      cwd: merged.cwd,
      env,
      stdio: ["pipe", "pipe", "pipe"],
      windowsHide: true,
    });
    const run = new Run(child, binary, merged.signal);
    child.stdin.end(encodePrompt(prompt) + "\n");
    return run;
  }

  /** Run a prompt to completion. Rejects with QGRunError on a non-success status. */
  run(prompt: string | PromptInput, options: RunOptions = {}): Promise<RunResult> {
    return this.stream(prompt, options).result();
  }
}

function encodePrompt(prompt: string | PromptInput): string {
  const input = typeof prompt === "string" ? { text: prompt } : prompt;
  const images = input.images?.map((image) => ({
    mediaType: image.mediaType,
    data: typeof image.data === "string" ? image.data : Buffer.from(image.data).toString("base64"),
  }));
  return JSON.stringify({
    schemaVersion: SCHEMA_VERSION,
    type: "message",
    role: "user",
    content: input.text,
    ...(images?.length ? { images } : {}),
  });
}
