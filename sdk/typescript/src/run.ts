import type { ChildProcessWithoutNullStreams } from "node:child_process";
import { createInterface } from "node:readline";
import { QGAbortError, QGBinaryNotFoundError, QGProtocolError, QGRunError } from "./errors.ts";
import { SCHEMA_VERSION, type PermissionDenial, type QGEvent, type RunResult } from "./types.ts";

const STDERR_LIMIT = 64 * 1024;
const KILL_GRACE_MS = 5000;

interface Exit {
  code: number | null;
  signal: NodeJS.Signals | null;
  error?: NodeJS.ErrnoException;
}

/**
 * A live `qg exec` process. Iterate it for events, or await `result()` for the
 * outcome. Both may be used together; the stream is single-consumer.
 */
export class Run implements AsyncIterable<QGEvent> {
  readonly #child: ChildProcessWithoutNullStreams;
  readonly #binary: string;
  readonly #exited: Promise<Exit>;
  #stderr = "";
  #aborted = false;
  #events?: AsyncGenerator<QGEvent, void>;

  #runId = "";
  #sessionId?: string;
  #model?: string;
  #final?: string;
  #text = "";
  #end?: { status: string; exitCode: number };
  #error?: { code?: string; message: string };
  #usage = { promptTokens: 0, completionTokens: 0, cachedInputTokens: 0, totalTokens: 0 } as RunResult["usage"];
  #changed = new Set<string>();
  #warnings: string[] = [];
  #pending = new Map<string, PermissionDenial>();

  constructor(child: ChildProcessWithoutNullStreams, binary: string, signal?: AbortSignal) {
    this.#child = child;
    this.#binary = binary;
    child.stderr.setEncoding("utf8");
    child.stderr.on("data", (chunk: string) => {
      this.#stderr = (this.#stderr + chunk).slice(-STDERR_LIMIT);
    });
    this.#exited = new Promise<Exit>((resolve) => {
      child.once("error", (error: NodeJS.ErrnoException) => resolve({ code: null, signal: null, error }));
      child.once("close", (code, sig) => resolve({ code, signal: sig }));
    });
    // A closed stdin (early exit) must not crash the host process.
    child.stdin.on("error", () => {});
    if (signal) {
      if (signal.aborted) this.abort();
      else signal.addEventListener("abort", () => this.abort(), { once: true });
    }
  }

  get pid(): number | undefined {
    return this.#child.pid;
  }

  /** Stop the run. SIGTERM first, SIGKILL if it does not exit. */
  abort(): void {
    if (this.#aborted) return;
    this.#aborted = true;
    this.#child.kill("SIGTERM");
    const timer = setTimeout(() => this.#child.kill("SIGKILL"), KILL_GRACE_MS);
    timer.unref();
    void this.#exited.then(() => clearTimeout(timer));
  }

  [Symbol.asyncIterator](): AsyncGenerator<QGEvent, void> {
    return (this.#events ??= this.#pump());
  }

  /** Drain the stream and return the outcome. Throws QGRunError on a non-success status. */
  async result(): Promise<RunResult> {
    for await (const _ of this) void _;
    await this.#assertEnded();
    const result = this.#snapshot();
    if (result.status !== "success" || result.exitCode !== 0) {
      throw new QGRunError(result, this.#error ?? {}, this.#stderr);
    }
    return result;
  }

  async *#pump(): AsyncGenerator<QGEvent, void> {
    const lines = createInterface({ input: this.#child.stdout, crlfDelay: Infinity });
    let drained = false;
    try {
      for await (const line of lines) {
        const trimmed = line.trim();
        if (!trimmed) continue;
        const event = this.#parse(trimmed);
        this.#observe(event);
        yield event;
      }
      drained = true;
    } finally {
      lines.close();
      // Consumer stopped early (break/throw): do not leave the process running.
      if (!drained) this.abort();
    }
    await this.#assertEnded();
  }

  /** Wait for the process and fail unless it delivered a run_end. */
  async #assertEnded(): Promise<void> {
    const exit = await this.#exited;
    if (exit.error?.code === "ENOENT") throw new QGBinaryNotFoundError(this.#binary, exit.error);
    if (exit.error) throw exit.error;
    if (this.#end) return;
    if (this.#aborted) throw new QGAbortError();
    throw new QGProtocolError(
      `qg exited (code ${exit.code ?? exit.signal}) before emitting run_end`,
      this.#stderr,
    );
  }

  #parse(line: string): QGEvent {
    let event: QGEvent;
    try {
      event = JSON.parse(line) as QGEvent;
    } catch {
      throw new QGProtocolError(`invalid stream-json line: ${line.slice(0, 200)}`, this.#stderr);
    }
    if (event.schemaVersion !== SCHEMA_VERSION) {
      throw new QGProtocolError(
        `unsupported schemaVersion ${event.schemaVersion} (SDK speaks ${SCHEMA_VERSION}); update @quebragalho-dev/sdk or qg`,
        this.#stderr,
      );
    }
    return event;
  }

  #observe(event: QGEvent): void {
    switch (event.type) {
      case "run_start":
        this.#runId = event.runId;
        this.#sessionId = event.sessionId;
        this.#model = event.model;
        break;
      case "text":
        this.#text += event.delta;
        break;
      case "final":
        this.#final = event.text;
        break;
      case "usage": {
        const u = this.#usage;
        u.promptTokens += event.promptTokens ?? 0;
        u.completionTokens += event.completionTokens ?? 0;
        u.cachedInputTokens += event.cachedInputTokens ?? 0;
        u.totalTokens += event.totalTokens ?? 0;
        if (event.costUsd !== undefined) u.costUsd = (u.costUsd ?? 0) + event.costUsd;
        if (event.costBrl !== undefined) u.costBrl = (u.costBrl ?? 0) + event.costBrl;
        break;
      }
      case "permission_request":
        if (event.id) {
          this.#pending.set(event.id, {
            id: event.id,
            tool: event.name ?? "",
            reason: event.reason,
            riskLevel: event.risk?.level,
            categories: event.risk?.categories ?? [],
          });
        }
        break;
      case "permission_decision":
        if (event.id && event.permissionGranted) this.#pending.delete(event.id);
        break;
      case "tool_result":
        for (const file of event.changedFiles ?? []) this.#changed.add(file);
        break;
      case "warning":
        this.#warnings.push(event.message);
        break;
      case "error":
        this.#error = { code: event.code, message: event.message };
        break;
      case "run_end":
        this.#end = { status: event.status, exitCode: event.exitCode };
        break;
    }
  }

  #snapshot(): RunResult {
    return {
      runId: this.#runId,
      sessionId: this.#sessionId,
      model: this.#model,
      text: this.#final ?? this.#text,
      status: this.#end?.status ?? "unknown",
      exitCode: this.#end?.exitCode ?? -1,
      usage: { ...this.#usage },
      changedFiles: [...this.#changed],
      warnings: [...this.#warnings],
      permissionDenials: [...this.#pending.values()],
    };
  }
}
