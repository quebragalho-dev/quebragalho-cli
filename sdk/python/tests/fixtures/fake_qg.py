#!/usr/bin/env python3
"""Stand-in for `qg exec -o stream-json`: behaviour chosen by FAKE_MODE."""
import json
import os
import sys
import time

mode = os.environ.get("FAKE_MODE", "ok")


def ev(**e):
    sys.stdout.write(json.dumps({"schemaVersion": 2, "runId": "run_1", **e}) + "\n")
    sys.stdout.flush()


if mode == "echo-args":
    prompt = json.loads(sys.stdin.readline())
    ev(type="run_start", sessionId="s1", model="m")
    ev(type="final", text=json.dumps({"argv": sys.argv[1:], "prompt": prompt, "key": os.environ.get("QG_API_KEY")}))
    ev(type="run_end", status="success", exitCode=0)
elif mode == "ok":
    sys.stdin.read()
    ev(type="run_start", sessionId="s1", model="m")
    ev(type="text", delta="hel")
    ev(type="text", delta="lo")
    ev(type="tool_result", id="c1", status="ok", changedFiles=["a.txt", "a.txt", "b.txt"])
    ev(type="usage", promptTokens=10, completionTokens=2, totalTokens=12, costUsd=0.5)
    ev(type="warning", message="careful")
    ev(type="final", text="hello")
    ev(type="run_end", status="success", exitCode=0)
elif mode == "provider-error":
    sys.stdin.read()
    ev(type="run_start")
    ev(type="error", code="provider_error", message="boom", recoverable=False)
    ev(type="run_end", status="error", exitCode=3)
    sys.exit(3)
elif mode == "no-run-end":
    sys.stdin.read()
    ev(type="run_start")
    sys.stderr.write("crashed\n")
    sys.exit(1)
elif mode == "garbage":
    print("not json", flush=True)
elif mode == "future-schema":
    print(json.dumps({"schemaVersion": 3, "type": "run_start", "runId": "r"}), flush=True)
elif mode == "hang":
    ev(type="run_start")
    time.sleep(60)
