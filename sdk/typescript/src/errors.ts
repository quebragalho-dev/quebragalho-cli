import type { RunResult } from "./types.ts";

export class QGError extends Error {
  constructor(message: string, options?: { cause?: unknown }) {
    super(message, options);
    this.name = new.target.name;
  }
}

/** The qg binary could not be started. */
export class QGBinaryNotFoundError extends QGError {
  readonly binary: string;
  constructor(binary: string, cause?: unknown) {
    super(
      `qg binary not found: "${binary}". Install it (npm i -g @quebragalho-dev/cli) or pass { binary } / set QG_BIN.`,
      { cause },
    );
    this.binary = binary;
  }
}

/** The process produced output that is not valid stream-json, or ended without run_end. */
export class QGProtocolError extends QGError {
  readonly stderr: string;
  constructor(message: string, stderr = "") {
    super(stderr ? `${message}\n${stderr.trim()}` : message);
    this.stderr = stderr;
  }
}

/** The run was cancelled through AbortSignal or Run.abort(). */
export class QGAbortError extends QGError {
  constructor() {
    super("qg run aborted");
  }
}

/** qg finished but reported a non-success status. */
export class QGRunError extends QGError {
  readonly exitCode: number;
  readonly status: string;
  readonly code?: string;
  readonly result: RunResult;
  readonly stderr: string;
  constructor(result: RunResult, detail: { code?: string; message?: string }, stderr: string) {
    super(detail.message || `qg run ended with status "${result.status}" (exit ${result.exitCode})`);
    this.exitCode = result.exitCode;
    this.status = result.status;
    this.code = detail.code;
    this.result = result;
    this.stderr = stderr;
  }
}
