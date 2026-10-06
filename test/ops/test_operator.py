import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
from pathlib import Path
import threading
import time
import unittest


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("casper_operator", ROOT / "scripts/operator.py")
operator = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(operator)


class StaticClient:
    def overview(self):
        return {
            "generated_at": "2026-10-06T00:00:00Z",
            "mode": "live",
            "health": {"control-plane": {"ok": True}},
            "fleet": [{"id": "node-1", "region": "eu", "role": "entry", "status": "active",
                       "transports": ["hysteria2", "vless-reality"], "created_at": "2026-10-06T00:00:00Z"}],
            "accounts": {"active": 1, "suspended": 0, "expired": 0, "banned": 0},
            "subscriptions": {"trialing": 0, "active": 1, "past_due": 0, "canceled": 0, "expired": 0},
            "account_recent": [],
            "billing": {"counts": {"pending": 0, "settled": 1, "expired": 0, "invalid": 0}, "recent": []},
            "plans": [],
            "errors": {},
        }


class OperatorHTTPTests(unittest.TestCase):
    def setUp(self):
        values = {
            "CP_ADMIN_TOKEN": "super-secret-admin-token",
            "BILLING_INTERNAL_TOKEN": "super-secret-billing-token",
        }
        self.config = operator.OperatorConfig(values, bind="127.0.0.1", port=0)
        self.server = operator.create_server(self.config, client=StaticClient())
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.port = self.server.server_address[1]

    def request(self, path="/api/overview", host=None, origin=None, method="GET"):
        connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=2)
        headers = {"Host": host or f"127.0.0.1:{self.port}"}
        if origin:
            headers["Origin"] = origin
        connection.request(method, path, headers=headers)
        response = connection.getresponse()
        body = response.read()
        connection.close()
        return response, body

    def test_rejects_unsafe_host_and_origin(self):
        response, _ = self.request(host="attacker.example")
        self.assertEqual(response.status, 421)
        response, _ = self.request(origin="https://attacker.example")
        self.assertEqual(response.status, 403)

    def test_api_is_read_only_and_never_exposes_server_tokens(self):
        response, body = self.request()
        self.assertEqual(response.status, 200)
        self.assertEqual(response.getheader("Access-Control-Allow-Origin"), None)
        self.assertNotIn(b"super-secret", body)
        self.assertEqual(json.loads(body)["mode"], "live")
        response, _ = self.request(method="POST")
        self.assertEqual(response.status, 405)


class ProjectionTests(unittest.TestCase):
    def test_projection_drops_node_and_account_credentials_and_pii(self):
        node = {
            "id": "node-1", "region": "eu", "role": "entry", "status": "active",
            "public_ip": "203.0.113.7", "private_key": "node-secret",
            "transports": [{"type": "vless-reality", "enabled": True, "config": {"password": "secret"}}],
            "created_at": "2026-10-06T00:00:00Z",
        }
        account_summary = {
            "users": {"active": 2, "suspended": 0, "expired": 1, "banned": 0},
            "subscriptions": {"trialing": 0, "active": 2, "past_due": 0, "canceled": 1, "expired": 0},
            "recent": [{"id": "user-1", "status": "active", "subscription_id": "sub-1",
                        "plan": "basic", "subscription_status": "active",
                        "expires_at": "2026-11-06T00:00:00Z", "updated_at": "2026-10-06T00:00:00Z",
                        "telegram_id": 1234, "email": "secret@example.com", "token": "secret"}],
        }
        projected = {
            "node": operator.project_node(node),
            "accounts": operator.project_account_summary(account_summary),
        }
        encoded = json.dumps(projected, sort_keys=True)
        for forbidden in ("public_ip", "private_key", "password", "telegram_id", "email", "token", "node-secret"):
            self.assertNotIn(forbidden, encoded)
        self.assertEqual(projected["node"]["transports"], ["vless-reality"])

    def test_explicit_demo_data_is_labeled(self):
        overview = operator.demo_overview()
        self.assertEqual(overview["mode"], "demo")
        self.assertTrue(overview["demo_notice"])


class RedirectTests(unittest.TestCase):
    def test_authorization_is_not_forwarded_to_redirect_target(self):
        received = []

        class TargetHandler(BaseHTTPRequestHandler):
            def do_GET(self):
                received.append(self.headers.get("Authorization"))
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b"{}")

            def log_message(self, _format, *_args):
                return

        target = ThreadingHTTPServer(("127.0.0.1", 0), TargetHandler)
        target_thread = threading.Thread(target=target.serve_forever, daemon=True)
        target_thread.start()
        self.addCleanup(target.server_close)
        self.addCleanup(target.shutdown)

        target_port = target.server_address[1]

        class RedirectHandler(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(302)
                self.send_header("Location", f"http://localhost:{target_port}/capture")
                self.end_headers()

            def log_message(self, _format, *_args):
                return

        redirect = ThreadingHTTPServer(("127.0.0.1", 0), RedirectHandler)
        redirect_thread = threading.Thread(target=redirect.serve_forever, daemon=True)
        redirect_thread.start()
        self.addCleanup(redirect.server_close)
        self.addCleanup(redirect.shutdown)

        values = {
            "CP_ADMIN_TOKEN": "synthetic-review-token",
            "BILLING_INTERNAL_TOKEN": "billing-test-token",
            "OPERATOR_CONTROL_PLANE_URL": f"http://127.0.0.1:{redirect.server_address[1]}",
        }
        client = operator.SafeAPIClient(operator.OperatorConfig(values))
        with self.assertRaises(operator.OperatorError):
            client._request("control-plane", "/v1/nodes", values["CP_ADMIN_TOKEN"])
        self.assertEqual(received, [])


class DeadlineTests(unittest.TestCase):
    def test_slow_unavailable_sections_share_one_bounded_deadline(self):
        class SlowHandler(BaseHTTPRequestHandler):
            def do_GET(self):
                time.sleep(0.4)
                try:
                    self.send_response(503)
                    self.end_headers()
                except (BrokenPipeError, ConnectionResetError):
                    pass

            def log_message(self, _format, *_args):
                return

        upstream = ThreadingHTTPServer(("127.0.0.1", 0), SlowHandler)
        thread = threading.Thread(target=upstream.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(upstream.server_close)
        self.addCleanup(upstream.shutdown)
        base = f"http://127.0.0.1:{upstream.server_address[1]}"
        values = {"CP_ADMIN_TOKEN": "admin", "BILLING_INTERNAL_TOKEN": "billing"}
        for service in ("CONTROL_PLANE", "SUBSCRIPTION", "DELIVERY", "BILLING", "TELEMETRY"):
            values[f"OPERATOR_{service}_URL"] = base
        client = operator.SafeAPIClient(operator.OperatorConfig(values, timeout=0.2, overview_deadline=1))
        started = time.monotonic()
        overview = client.overview()
        elapsed = time.monotonic() - started
        self.assertLess(elapsed, 0.8)
        self.assertEqual(set(overview["errors"]), {"fleet", "accounts", "billing", "plans"})
        self.assertTrue(all(not state["ok"] for state in overview["health"].values()))


if __name__ == "__main__":
    unittest.main()
