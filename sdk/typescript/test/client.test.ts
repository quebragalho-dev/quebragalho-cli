import assert from "node:assert/strict";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import {
  QG,
  QGAbortError,
  QGBinaryNotFoundError,
  QGProtocolError,
  QGRunError,
  buildArgs,
  type QGEvent,
} from "../src/index.ts";

const fake = fileURLToPath(new URL("./fixtures/fake-qg.mjs", import.meta.url));
const qg = (mode: string, extra: Record<string, string> = {}) =>
  new QG({ binary: fake, env: { FAKE_MODE: mode, ...extra } });

test("run aggregates text, usage, changed files and warnings", async () => {
  const result = await qg("ok").run("hi");
  assert.equal(result.text, "hello");
  assert.equal(result.status, "success");
  assert.equal(result.exitCode, 0);
  assert.equal(result.sessionId, "s1");
  assert.deepEqual(result.usage, {
    promptTokens: 10,
    completionTokens: 2,
    cachedInputTokens: 0,
    totalTokens: 12,
    costUsd: 0.5,
    costBrl: 2.5,
  });
  assert.deepEqual(result.permissionDenials, [
    { id: "p1", tool: "exec_command", reason: "network access requires approval", riskLevel: "critical", categories: ["network", "shell"] },
  ]);
  assert.deepEqual(result.changedFiles, ["a.txt", "b.txt"]);
  assert.deepEqual(result.warnings, ["careful"]);
});

test("stream yields typed events in order", async () => {
  const types: string[] = [];
  const deltas: string[] = [];
  const run = qg("ok").stream("hi");
  for await (const event of run) {
    types.push(event.type);
    if (event.type === "text") deltas.push(event.delta);
  }
  assert.equal(types[0], "run_start");
  assert.equal(types.at(-1), "run_end");
  assert.equal(deltas.join(""), "hello");
  assert.equal((await run.result()).text, "hello");
});

test("prompt goes on stdin, key in env, flags on argv", async () => {
  const result = await qg("echo-args").run("segredo? não", {
    model: "kimi-k3",
    plan: true,
    enabledTools: ["read_file", "grep"],
    resume: true,
    apiKey: undefined,
  } as never);
  const seen = JSON.parse(result.text);
  assert.equal(seen.prompt.content, "segredo? não");
  assert.equal(seen.prompt.type, "message");
  assert.ok(!seen.argv.includes("segredo? não"));
  assert.deepEqual(seen.argv.slice(0, 5), ["exec", "--input-format", "stream-json", "--output-format", "stream-json"]);
  assert.ok(seen.argv.includes("--plan"));
  assert.equal(seen.argv[seen.argv.indexOf("--enabled-tools") + 1], "read_file,grep");
  assert.equal(seen.key, null);

  const keyed = await new QG({ binary: fake, apiKey: "qg-sk-test", env: { FAKE_MODE: "echo-args" } }).run("x");
  assert.equal(JSON.parse(keyed.text).key, "qg-sk-test");
  assert.ok(!JSON.parse(keyed.text).argv.includes("qg-sk-test"));
});

test("images are base64-encoded", async () => {
  const result = await qg("echo-args").run({
    text: "see",
    images: [{ mediaType: "image/png", data: new Uint8Array([1, 2, 3]) }],
  });
  assert.deepEqual(JSON.parse(result.text).prompt.images, [{ mediaType: "image/png", data: "AQID" }]);
});

test("non-success status rejects with QGRunError carrying detail", async () => {
  await assert.rejects(qg("provider-error").run("x"), (err: unknown) => {
    assert.ok(err instanceof QGRunError);
    assert.equal(err.exitCode, 3);
    assert.equal(err.code, "provider_error");
    assert.equal(err.message, "boom");
    assert.equal(err.result.status, "error");
    return true;
  });
});

test("missing run_end is a protocol error with stderr", async () => {
  await assert.rejects(qg("no-run-end").run("x"), (err: unknown) => {
    assert.ok(err instanceof QGProtocolError);
    assert.match(err.message, /crashed/);
    return true;
  });
});

test("garbage and future schema versions are rejected", async () => {
  await assert.rejects(qg("garbage").run("x"), QGProtocolError);
  await assert.rejects(qg("future-schema").run("x"), /unsupported schemaVersion 3/);
});

test("missing binary gives an actionable error", async () => {
  await assert.rejects(new QG({ binary: "/nonexistent/qg" }).run("x"), QGBinaryNotFoundError);
});

test("AbortSignal stops a hung run", async () => {
  const controller = new AbortController();
  const run = qg("hang").stream("x", { signal: controller.signal });
  const seen: QGEvent[] = [];
  const done = (async () => {
    for await (const event of run) {
      seen.push(event);
      controller.abort();
    }
  })();
  await assert.rejects(done, QGAbortError);
  assert.equal(seen[0]?.type, "run_start");
});

test("breaking out of the stream kills the process", async () => {
  const run = qg("hang").stream("x");
  for await (const event of run) {
    assert.equal(event.type, "run_start");
    break;
  }
  await assert.rejects(run.result(), QGAbortError);
});

test("buildArgs omits unset options", () => {
  assert.deepEqual(buildArgs({}), ["exec", "--input-format", "stream-json", "--output-format", "stream-json"]);
  assert.deepEqual(buildArgs({ addDirs: ["/a", "/b"], worktree: "wt", fork: "s9" }).slice(5), [
    "--add-dir", "/a", "--add-dir", "/b", "--fork", "s9", "--worktree", "wt",
  ]);
});
