"""Updater HTTP client fixtures only; no socket, installed service or native operation.

The in-memory HTTP transport checks session/header construction, not product
authorization. Shipping CSRF and installed activation retain separate evidence.
"""
import contextlib
from email.message import Message
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock
import urllib.request
import urllib.response

import updater_api as updater


class SessionTransport(urllib.request.BaseHandler):
    handler_order = 100

    def __init__(self):
        self.requests = []
        self.accepted = 0
        self.reject_apply = False
        self.expose_token = True

    def http_open(self, request):
        cookie = request.get_header("Cookie")
        token = {"fixture-session=A": "fixture-private-csrf-A",
                 "fixture-session=B": "fixture-private-csrf-B"}[cookie]
        path = request.selector
        self.requests.append((path, cookie, request.get_header("X-csrf-token")))
        if path == "/api/v1/auth/me":
            status, body = 200, {"user": {"id": 1, "role": "admin"}}
            if self.expose_token:
                body["csrf_token"] = token
        elif path == "/api/v1/system/update/apply":
            if json.loads(request.data) != {"stage_id": "a" * 64, "confirm": True}:
                raise AssertionError("Activation input differs")
            if request.get_header("X-csrf-token") == token and not self.reject_apply:
                self.accepted += 1
                status, body = 202, {"status": "accepted"}
            else:
                status, body = 403, {"error": "fixture-private-rejection-body"}
        else:
            raise AssertionError("Unexpected fixture route; no network fallback")
        headers = Message()
        headers["Content-Type"] = "application/json"
        response = urllib.response.addinfourl(io.BytesIO(json.dumps(body).encode()), headers, request.full_url, status)
        response.msg = "fixture HTTP response"
        return response


class UpdaterClientTests(unittest.TestCase):
    def session(self, cookie="fixture-session=A"):
        session = updater.client()
        transport = SessionTransport()
        session.add_handler(transport)
        session.addheaders = [("Cookie", cookie)]
        return session, transport

    def test_missing_and_wrong_session_csrf_rejected(self):
        session, transport = self.session()
        for token in (None, "fixture-private-csrf-B"):
            session.addheaders = [("Cookie", "fixture-session=A")]
            if token is not None:
                session.addheaders.append(("X-CSRF-Token", token))
            updater.request(session, "/api/v1/system/update/apply", {"stage_id": "a" * 64, "confirm": True}, 403)
        self.assertEqual(transport.accepted, 0)

    def test_apply_fetches_actual_session_token_and_sends_it_only_in_memory(self):
        for cookie in ("fixture-session=A", "fixture-session=B"):
            with self.subTest(cookie=cookie):
                session, transport = self.session(cookie)
                session.addheaders.append(("x-CsRf-ToKeN", "stale-private-token"))
                original = list(session.addheaders)
                with contextlib.redirect_stdout(io.StringIO()) as stdout, contextlib.redirect_stderr(io.StringIO()) as stderr:
                    result = updater.apply_update(session, "a" * 64)
                self.assertEqual(result, {"status": "accepted"})
                self.assertEqual(transport.accepted, 1)
                self.assertEqual([item[0] for item in transport.requests], ["/api/v1/auth/me", "/api/v1/system/update/apply"])
                self.assertEqual(transport.requests[-1][1], cookie)
                self.assertEqual(transport.requests[-1][2], "fixture-private-csrf-" + cookie[-1])
                self.assertEqual(session.addheaders, original, "temporary token headers leaked into later client requests")
                self.assertEqual(stdout.getvalue() + stderr.getvalue(), "")

    def test_failed_apply_restores_headers_and_does_not_log_token_or_body(self):
        session, transport = self.session()
        # A mutation failure exercises the real request helper's fixed status
        # diagnostic; its raw rejection body/token must remain private.
        transport.reject_apply = True
        session.addheaders.append(("X-CSRF-Token", "fixture-private-csrf-B"))
        original = list(session.addheaders)
        with contextlib.redirect_stdout(io.StringIO()) as stdout, contextlib.redirect_stderr(io.StringIO()) as stderr:
            with self.assertRaisesRegex(RuntimeError, "unexpected HTTP status at /api/v1/system/update/apply: 403") as caught:
                updater.apply_update(session, "a" * 64)
        self.assertNotIn("fixture-private", str(caught.exception))
        self.assertEqual(stdout.getvalue() + stderr.getvalue(), "")
        self.assertEqual(transport.accepted, 0)
        self.assertEqual(session.addheaders, original)

    def test_missing_token_response_refuses_before_mutation(self):
        session, transport = self.session()
        transport.expose_token = False
        original = list(session.addheaders)
        with self.assertRaisesRegex(RuntimeError, "^session CSRF token unavailable$"):
            updater.apply_update(session, "a" * 64)
        self.assertEqual([item[0] for item in transport.requests], ["/api/v1/auth/me"])
        self.assertEqual(transport.accepted, 0)
        self.assertEqual(session.addheaders, original)


class UpdaterDiagnosticTests(unittest.TestCase):
    def test_authored_windows_availability_reason_is_retained_without_raw_text(self):
        evidence = updater.safe_initial_status({"disabled_reason": "Native updater unavailable (Windows code=0x4e080005)."})
        self.assertEqual(evidence["disabled_reason"], "windows_native_unavailable")
        self.assertEqual(evidence["windows_availability"], {"stage": "process_open", "win32_code": 5})

    def test_availability_diagnostics_reject_unknown_or_arbitrary_reason_values(self):
        for reason in ("private", None, ["private"], "Native updater unavailable (Windows code=0x4e000005).",
                       "Native updater unavailable (Windows code=0x4e1d0005).",
                       "Native updater unavailable (Windows code=0x4eff0005).",
                       "Native updater unavailable (Windows code=0x4d080005).",
                       "Native updater unavailable (Windows code=0x4E080005).",
                       "Native updater unavailable (Windows code=0x4e080005).private",
                       "Native updater unavailable (Windows code=0x4e080005).\n",
                       "Native updater unavailable (Windows code=0x4e0800005)."):
            evidence = updater.safe_initial_status({"disabled_reason": reason})
            self.assertEqual(evidence["disabled_reason"], "unrecognized")
            self.assertNotIn("windows_availability", evidence)
            self.assertNotIn("private", json.dumps(evidence))
        for code, expected in (("4e020005", {"stage": "pipe_open", "win32_code": 5}),
                               ("4e160002", {"stage": "server_trust", "win32_code": 2}),
                               ("4e19ffff", {"stage": "server_pending", "win32_code": 65535}),
                               ("4e1c0000", {"stage": "internal", "win32_code": 0})):
            evidence = updater.safe_initial_status({"disabled_reason": "Native updater unavailable (Windows code=0x" + code + ")."})
            self.assertEqual(evidence["windows_availability"], expected)

    def test_unexpected_terminal_is_reported_without_waiting_for_timeout(self):
        with tempfile.TemporaryDirectory() as temp:
            state = Path(temp)
            (state / "activation.json").write_text(json.dumps({"request": {"stage_id": "a" * 64}, "phase": "aborted"}))
            with mock.patch.object(updater.time, "monotonic", side_effect=[0, 0, 311]), mock.patch.object(updater.time, "sleep"):
                with self.assertRaisesRegex(RuntimeError, "^unexpected native updater terminal state$"):
                    updater.wait_terminal(state, "a" * 64, "healthy")

    def test_retryable_partial_json_can_reach_expected_terminal(self):
        with tempfile.TemporaryDirectory() as temp:
            state = Path(temp)
            journal = state / "activation.json"
            journal.write_text("{")
            expected = {"request": {"stage_id": "a" * 64}, "phase": "healthy"}
            with mock.patch.object(updater.time, "monotonic", side_effect=[0, 0, 1]), mock.patch.object(
                    updater.time, "sleep", side_effect=lambda _: journal.write_text(json.dumps(expected))):
                self.assertEqual(updater.wait_terminal(state, "a" * 64, "healthy"), expected)

    def test_timeout_retains_last_safe_phase_with_no_control_secrets(self):
        with tempfile.TemporaryDirectory() as temp:
            state = Path(temp)
            private = "private-must-never-appear"
            journal = {"request": {"stage_id": "a" * 64, "actor_id": 123, "requested_at": private},
                       "phase": "replacing", "nonce": private, "error": private, "rollback_attempted": False,
                       "prior": {"version": "v0.0.1", "schema": 19, "sha256": "b" * 64},
                       "candidate": {"version": "v0.0.2", "schema": 19, "sha256": "c" * 64}}
            (state / "activation.json").write_text(json.dumps(journal))
            with mock.patch.object(updater.time, "monotonic", side_effect=[0, 311]):
                with self.assertRaisesRegex(RuntimeError, "^native updater terminal state timed out$"):
                    updater.wait_terminal(state, "a" * 64, "healthy", state)
            body = (state / "activation-state-summary.json").read_text()
            evidence = json.loads(body)
            self.assertEqual(evidence, {"journal": "present", "stage_id": "a" * 64, "phase": "replacing",
                                       "rollback_attempted": False, "prior": journal["prior"], "candidate": journal["candidate"]})
            self.assertNotIn(private, body)
            self.assertNotIn("actor", body)
            self.assertNotIn("nonce", body)

    def test_aborted_terminal_retains_phase_and_fails_immediately(self):
        with tempfile.TemporaryDirectory() as temp:
            state = Path(temp)
            (state / "activation.json").write_text(json.dumps({"request": {"stage_id": "a" * 64}, "phase": "aborted"}))
            with mock.patch.object(updater.time, "monotonic", side_effect=[0, 0]):
                with self.assertRaisesRegex(RuntimeError, "^unexpected native updater terminal state$"):
                    updater.wait_terminal(state, "a" * 64, "healthy", state)
            self.assertEqual(json.loads((state / "activation-state-summary.json").read_text())["phase"], "aborted")

    def test_journal_diagnostics_bound_input_and_filter_arbitrary_values(self):
        with tempfile.TemporaryDirectory() as temp:
            state = Path(temp)
            path = state / "activation.json"
            cases = [(None, "absent"), ("{", "malformed"), ("x" * 16385, "oversized"),
                     ("[]", "malformed"), ("[" * 2000 + "]" * 2000, "malformed")]
            for raw, expected in cases:
                if raw is not None:
                    path.write_text(raw)
                updater.write_journal_evidence(state, state)
                self.assertEqual(json.loads((state / "activation-state-summary.json").read_text()), {"journal": expected})
            path.write_text(json.dumps({"phase": ["private"], "request": {"stage_id": "private"},
                                        "rollback_attempted": "private", "prior": {"version": "private", "schema": True, "sha256": "Z" * 64},
                                        "candidate": "private", "nonce": "private"}))
            updater.write_journal_evidence(state, state)
            body = (state / "activation-state-summary.json").read_text()
            self.assertNotIn("private", body)
            self.assertEqual(json.loads(body)["prior"], {"version": "unexpected", "schema": None, "sha256": None})
            self.assertEqual(json.loads(body)["phase"], "unrecognized")

    def test_initial_status_keeps_primitive_allowlist_and_fixed_reason_only(self):
        self.assertEqual(updater.safe_initial_status({"current_version": "v0.0.1", "schema": 19,
            "trust_configured": True, "activation_available": False,
            "disabled_reason": "The native update and recovery service is not installed or ready.",
            "stages": [{"private": "private"}]}), {"current_version": "v0.0.1", "schema": 19,
            "trust_configured": True, "activation_available": False, "disabled_reason": "native_not_ready"})
        body = json.dumps(updater.safe_initial_status({"current_version": "private", "schema": True,
            "trust_configured": "private", "activation_available": {"private": "private"}, "disabled_reason": ["private"]}))
        self.assertNotIn("private", body)
        self.assertEqual(json.loads(body), {"current_version": "unexpected", "schema": None,
            "trust_configured": None, "activation_available": None, "disabled_reason": "unrecognized"})


if __name__ == "__main__":
    unittest.main()
