import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from urllib.error import HTTPError

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("launch", ROOT / "scripts/launch.py")
launch = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(launch)


class LaunchTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = Path(self.directory.name) / "production.env"
        launch.initialize(self.path)
        self.values = launch.read_env(self.path)
        self.values.update(
            PUBLIC_HOST="launch.caspervpn-check.com", TLS_EMAIL="test@example.org",
            BTCPAY_BASE_URL="https://btcpay.caspervpn-check.com",
            BTCPAY_API_KEY="test-only", BTCPAY_STORE_ID="test-only", BTCPAY_WEBHOOK_SECRET="test-only",
            DELIVERY_TELEGRAM_TOKEN="test-only",
            BILLING_PLAN_CATALOG_HOST=str(ROOT / "services/billing/config/plans.example.json"),
        )

    def test_generated_secrets_private_unique_and_never_overwritten(self):
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o600)
        original = self.path.read_bytes()
        with self.assertRaises(FileExistsError):
            launch.initialize(self.path)
        self.assertEqual(self.path.read_bytes(), original)
        launch.validate(self.values)

    def test_incomplete_launch_and_mock_are_rejected(self):
        with self.assertRaises(ValueError):
            launch.validate(launch.read_env(self.path))
        self.values["BILLING_MOCK_SECRET"] = "test-only"
        with self.assertRaisesRegex(ValueError, "mock"):
            launch.validate(self.values)

    def test_duplicate_role_keys_rejected(self):
        self.values["CP_ADMIN_TOKEN"] = self.values["CP_BILLING_TOKEN"]
        with self.assertRaisesRegex(ValueError, "distinct"):
            launch.validate(self.values)

    def test_world_readable_settings_rejected(self):
        os.chmod(self.path, 0o644)
        with self.assertRaisesRegex(ValueError, "0600"):
            launch.read_env(self.path)

    def test_no_shell_expansion_in_private_settings(self):
        self.path.write_text("POSTGRES_PASSWORD=$(some-command)\n")
        with self.assertRaisesRegex(ValueError, "expansion"):
            launch.read_env(self.path)

    def test_currency_requires_matching_gateway_and_plan(self):
        self.values["DELIVERY_BOT_DEFAULT_CURRENCY"] = "XMR"
        with self.assertRaisesRegex(ValueError, "currency"):
            launch.validate(self.values)

    def test_proxy_network_must_be_private_and_stack_sized(self):
        for subnet in ("0.0.0.0/0", "8.8.8.0/24", "172.24.0.0/16"):
            with self.subTest(subnet=subnet):
                self.values["SERVICE_SUBNET"] = subnet
                with self.assertRaisesRegex(ValueError, "private"):
                    launch.validate(self.values)

    def test_fleet_gate_accepts_contract_transport_names(self):
        stack = launch.Stack.__new__(launch.Stack)
        stack.values = self.values
        nodes = [{"role": "entry", "status": "active", "transports": [
            {"type": "vless-reality", "enabled": True}, {"type": "hysteria2", "enabled": True}]}]
        with patch.object(launch, "urlopen", return_value=io.BytesIO(json.dumps({"items": nodes}).encode())):
            stack.fleet_ready()
        for invalid in ([], [{**nodes[0], "status": "provisioning"}],
                        [{**nodes[0], "transports": nodes[0]["transports"][:1]}]):
            with patch.object(launch, "urlopen", return_value=io.BytesIO(json.dumps({"items": invalid}).encode())):
                with self.assertRaisesRegex(ValueError, "active entries"):
                    stack.fleet_ready()

    def test_publish_does_not_start_telegram_before_valid_public_https(self):
        stack = launch.Stack.__new__(launch.Stack)
        events = []
        stack.run = lambda *args: events.append(args)
        stack.fleet_ready = lambda: events.append(("fleet",))
        stack.ready = lambda *args: events.append(("ready",))
        def public_probe():
            events.append(("public-https",))
            raise ValueError("invalid TLS")
        stack.edge_ready = public_probe
        with self.assertRaisesRegex(ValueError, "TLS"):
            stack.publish()
        self.assertEqual(events[0], ("stop", "delivery"))
        self.assertIn(("up", "-d", "edge"), events)
        self.assertNotIn(("up", "-d", "delivery"), events)
        stack.edge_ready = lambda: events.append(("public-https",))
        stack.publish()
        self.assertGreater(events.index(("up", "-d", "delivery")), events.index(("public-https",)))

    def test_edge_probe_requires_application_denial_with_valid_tls(self):
        stack = launch.Stack.__new__(launch.Stack)
        stack.values = self.values
        error = HTTPError("https://test", 401, "denied", {}, io.BytesIO(b'{"error":"unknown or revoked token"}'))
        with patch.object(launch, "urlopen", side_effect=error) as client:
            stack.edge_ready(timeout=1)
            self.assertTrue(client.call_args.args[0].startswith("https://"))
        with self.assertRaisesRegex(ValueError, "DNS/TLS"):
            stack.edge_ready(timeout=0)


if __name__ == "__main__":
    unittest.main()
