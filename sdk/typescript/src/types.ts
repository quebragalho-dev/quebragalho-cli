/** Stream-JSON schema version this SDK understands (docs/STREAM_JSON_PROTOCOL.md). */
export const SCHEMA_VERSION = 2;

export interface Risk {
  level: string;
  categories?: string[];
}

export interface Block {
  code: string;
  toolName?: string;
  action?: string;
  risk?: Risk;
  reason?: string;
  recoverable?: boolean;
}

interface Base {
  schemaVersion: number;
  runId: string;
}

export interface RunStartEvent extends Base {
  type: "run_start";
  sessionId?: string;
  cwd?: string;
  provider?: string;
  model?: string;
  apiModel?: string;
}
export interface TextEvent extends Base {
  type: "text";
  delta: string;
}
export interface ReasoningEvent extends Base {
  type: "reasoning";
  delta: string;
}
export interface ToolCallEvent extends Base {
  type: "tool_call";
  id: string;
  name: string;
  args?: unknown;
  sideEffect?: string;
}
export interface PermissionEvent extends Base {
  type: "permission" | "permission_request" | "permission_decision";
  id?: string;
  name?: string;
  action?: string;
  permission?: string;
  permissionGranted?: boolean;
  permissionMode?: string;
  sideEffect?: string;
  reason?: string;
  decisionReason?: string;
  risk?: Risk;
  block?: Block;
}
export interface ToolResultEvent extends Base {
  type: "tool_result";
  id: string;
  status?: string;
  output?: string;
  truncated?: boolean;
  redacted?: boolean;
  changedFiles?: string[];
  display?: { summary?: string; kind?: string };
}
export interface CheckpointEvent extends Base {
  type: "checkpoint" | "restore";
  checkpoint?: {
    sequence?: number;
    tool?: string;
    files?: string[];
    filesRestored?: number;
    filesDeleted?: number;
    skipped?: string[];
  };
}
export interface UsageEvent extends Base {
  type: "usage";
  promptTokens?: number;
  completionTokens?: number;
  cachedInputTokens?: number;
  cacheWriteTokens?: number;
  totalTokens?: number;
  /** Per-call price from the live QG catalog; absent when the model has no catalog price. */
  costUsd?: number;
  costBrl?: number;
}
export interface FinalEvent extends Base {
  type: "final";
  text: string;
}
export interface WarningEvent extends Base {
  type: "warning";
  message: string;
  code?: string;
}
export interface ErrorEvent extends Base {
  type: "error";
  code?: string;
  message: string;
  recoverable?: boolean;
}
export interface RunEndEvent extends Base {
  type: "run_end";
  status: string;
  exitCode: number;
}

export type QGEvent =
  | RunStartEvent
  | TextEvent
  | ReasoningEvent
  | ToolCallEvent
  | PermissionEvent
  | ToolResultEvent
  | CheckpointEvent
  | UsageEvent
  | FinalEvent
  | WarningEvent
  | ErrorEvent
  | RunEndEvent;

export interface Usage {
  promptTokens: number;
  completionTokens: number;
  cachedInputTokens: number;
  totalTokens: number;
  /** Run cost from the QG catalog pricing; undefined when no call could be priced. */
  costUsd?: number;
  costBrl?: number;
}

/** A tool call that needed approval, which a headless run cannot give. */
export interface PermissionDenial {
  id: string;
  tool: string;
  reason?: string;
  riskLevel?: string;
  categories: string[];
}

export interface RunResult {
  runId: string;
  sessionId?: string;
  model?: string;
  /** Final answer text. */
  text: string;
  status: string;
  exitCode: number;
  usage: Usage;
  /** Files changed by tool calls, deduplicated. */
  changedFiles: string[];
  warnings: string[];
  /** Calls blocked for lack of approval. Re-run with `permissionMode`/`auto`, or narrow the task. */
  permissionDenials: PermissionDenial[];
}

export interface ImageInput {
  mediaType: string;
  /** Raw bytes or an already base64-encoded string. */
  data: Uint8Array | string;
}

export interface PromptInput {
  text: string;
  images?: ImageInput[];
}

export interface QGOptions {
  /** Path to the qg binary. Defaults to `$QG_BIN`, then `qg` on PATH. */
  binary?: string;
  /** Sent as QG_API_KEY (never on the command line). Defaults to the stored login. */
  apiKey?: string;
  /** Default workspace directory for every run. */
  cwd?: string;
  /** Extra environment variables for the child process. */
  env?: Record<string, string | undefined>;
  /** Defaults merged under each call's options. */
  defaults?: RunOptions;
}

export interface RunOptions {
  model?: string;
  cwd?: string;
  /** Preset: smart, deep, fast, large, precise. */
  mode?: string;
  /** Read-only planning mode. */
  plan?: boolean;
  auto?: "low" | "medium" | "high";
  permissionMode?: "plan" | "spec-draft" | "auto" | "member" | "ask" | "unsafe";
  maxTurns?: number;
  reasoningEffort?: string;
  execProfile?: "balanced" | "fast" | "thorough";
  enabledTools?: string[];
  disabledTools?: string[];
  addDirs?: string[];
  /** Resume a session by id, or `true` for the latest. */
  resume?: string | true;
  fork?: string;
  sessionTitle?: string;
  /** Run inside an isolated git worktree (optionally named). */
  worktree?: string | true;
  /** Allow prompt-gated tools without approval. Dangerous. */
  dangerouslySkipPermissions?: boolean;
  /** Raw extra flags appended to `qg exec`. */
  extraArgs?: string[];
  signal?: AbortSignal;
}
