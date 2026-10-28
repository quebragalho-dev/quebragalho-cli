import asyncio
import json
import os
import unittest

from quebragalho import (
    QG,
    QGAbortError,
    QGBinaryNotFoundError,
    QGProtocolError,
    QGRunError,
    build_args,
)

FAKE = os.path.join(os.path.dirname(__file__), "fixtures", "fake_qg.py")


def qg(mode, **kw):
    return QG(binary=FAKE, env={"FAKE_MODE": mode}, **kw)


class SyncTests(unittest.TestCase):
    def test_run_aggregates(self):
        r = qg("ok").run("hi")
        self.assertEqual((r.text, r.status, r.exit_code, r.session_id), ("hello", "success", 0, "s1"))
        self.assertEqual(r.usage.total_tokens, 12)
        self.assertEqual((r.usage.cost_usd, r.usage.cost_brl), (0.5, 2.5))
        self.assertEqual(len(r.permission_denials), 1)
        d = r.permission_denials[0]
        self.assertEqual((d.id, d.tool, d.risk_level, d.categories), ("p1", "exec_command", "critical", ["network", "shell"]))
        self.assertEqual(r.changed_files, ["a.txt", "b.txt"])
        self.assertEqual(r.warnings, ["careful"])
        self.assertTrue(r.ok)

    def test_stream_events(self):
        run = qg("ok").stream("hi")
        events = list(run)
        self.assertEqual(events[0]["type"], "run_start")
        self.assertEqual(events[-1]["type"], "run_end")
        self.assertEqual("".join(e["delta"] for e in events if e["type"] == "text"), "hello")
        self.assertEqual(run.result().text, "hello")

    def test_prompt_on_stdin_key_in_env(self):
        r = qg("echo-args", api_key="qg-sk-test").run("segredo? não", model="kimi-k3", plan=True, enabled_tools=["a", "b"])
        seen = json.loads(r.text)
        self.assertEqual(seen["prompt"]["content"], "segredo? não")
        self.assertNotIn("segredo? não", seen["argv"])
        self.assertNotIn("qg-sk-test", seen["argv"])
        self.assertEqual(seen["key"], "qg-sk-test")
        self.assertIn("--plan", seen["argv"])
        self.assertEqual(seen["argv"][seen["argv"].index("--enabled-tools") + 1], "a,b")

    def test_images_base64(self):
        r = qg("echo-args").run({"text": "see", "images": [{"mediaType": "image/png", "data": b"\x01\x02\x03"}]})
        self.assertEqual(json.loads(r.text)["prompt"]["images"], [{"mediaType": "image/png", "data": "AQID"}])

    def test_run_error(self):
        with self.assertRaises(QGRunError) as cm:
            qg("provider-error").run("x")
        self.assertEqual((cm.exception.exit_code, cm.exception.code, str(cm.exception)), (3, "provider_error", "boom"))

    def test_protocol_errors(self):
        with self.assertRaisesRegex(QGProtocolError, "crashed"):
            qg("no-run-end").run("x")
        with self.assertRaises(QGProtocolError):
            qg("garbage").run("x")
        with self.assertRaisesRegex(QGProtocolError, "schemaVersion 3"):
            qg("future-schema").run("x")

    def test_missing_binary(self):
        with self.assertRaises(QGBinaryNotFoundError):
            QG(binary="/nonexistent/qg").run("x")

    def test_abort(self):
        run = qg("hang").stream("x")
        with self.assertRaises(QGAbortError):
            for event in run:
                self.assertEqual(event["type"], "run_start")
                run.abort()

    def test_break_kills_process(self):
        with qg("hang").stream("x") as run:
            for _ in run:
                break
        self.assertIsNotNone(run._proc.returncode)

    def test_build_args(self):
        self.assertEqual(build_args(), ["exec", "--input-format", "stream-json", "--output-format", "stream-json"])
        self.assertEqual(build_args(add_dirs=["/a"], fork="s9", worktree="wt")[5:], ["--fork", "s9", "--add-dir", "/a", "--worktree", "wt"])
        with self.assertRaises(TypeError):
            build_args(modle="x")


class AsyncTests(unittest.IsolatedAsyncioTestCase):
    async def test_arun(self):
        r = await qg("ok").arun("hi")
        self.assertEqual(r.text, "hello")

    async def test_astream(self):
        run = await qg("ok").astream("hi")
        types = [e["type"] async for e in run]
        self.assertEqual((types[0], types[-1]), ("run_start", "run_end"))
        self.assertTrue((await run.result()).ok)

    async def test_errors(self):
        with self.assertRaises(QGRunError):
            await qg("provider-error").arun("x")
        with self.assertRaises(QGProtocolError):
            await qg("no-run-end").arun("x")
        with self.assertRaises(QGBinaryNotFoundError):
            await QG(binary="/nonexistent/qg").arun("x")

    async def test_cancel_kills(self):
        run = await qg("hang").astream("x")
        async for _ in run:
            break
        run.abort()
        with self.assertRaises(QGAbortError):
            await run.result()


if __name__ == "__main__":
    unittest.main()
