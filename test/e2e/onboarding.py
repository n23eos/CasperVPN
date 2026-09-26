#!/usr/bin/env python3
"""Self-contained local PostgreSQL + HTTP onboarding acceptance, no external accounts."""
import argparse
import base64
from datetime import datetime, timezone
import hashlib
import hmac
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import secrets
import socket
import subprocess
import tempfile
import threading
import time
from urllib.error import HTTPError
from urllib.parse import parse_qs
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[2]


def request(url, body=None, token=None, method=None, headers=None):
    data = None if body is None else json.dumps(body, separators=(",", ":")).encode()
    values = {"Content-Type": "application/json", **(headers or {})}
    if token:
        values["Authorization"] = "Bearer " + token
    with urlopen(Request(url, data=data, method=method, headers=values), timeout=5) as response:
        payload = response.read()
        try:
            return json.loads(payload)
        except ValueError:
            return payload


def wait_for(check, description, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                return value
        except (OSError, ValueError, KeyError):
            pass
        time.sleep(0.1)
    raise AssertionError("timed out: " + description)


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class FakeTelegram:
    def __init__(self):
        self.updates = []
        self.messages = []
        self.offsets = []
        self.lock = threading.Lock()
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_POST(self):
                data = parse_qs(self.rfile.read(int(self.headers.get("Content-Length", "0"))).decode())
                if self.path == "/botlocal-test-token/getUpdates":
                    offset = int(data.get("offset", ["0"])[0])
                    with owner.lock:
                        owner.offsets.append(offset)
                        result = [item for item in owner.updates if item["update_id"] >= offset]
                    if not result:
                        time.sleep(0.1)
                elif self.path == "/botlocal-test-token/sendMessage":
                    with owner.lock:
                        owner.messages.append({"chat_id": int(data["chat_id"][0]), "text": data["text"][0]})
                    result = {"message_id": len(owner.messages)}
                else:
                    self.send_error(404)
                    return
                payload = json.dumps({"ok": True, "result": result}).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.server.daemon_threads = True
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.url = "http://127.0.0.1:" + str(self.server.server_port)

    def send(self, update_id, text, sender=900001, chat_type="private", chat_id=None):
        with self.lock:
            self.updates.append({"update_id": update_id, "message": {
                "from": {"id": sender}, "chat": {"id": chat_id or sender, "type": chat_type}, "text": text}})

    def reply(self, start, contains, chat=900001):
        with self.lock:
            return next((m["text"] for m in self.messages[start:] if m["chat_id"] == chat and contains in m["text"]), None)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin-dir", type=Path, default=ROOT / "bin")
    parser.add_argument("--integration", action="store_true", help="also run isolated PostgreSQL integration suites")
    args = parser.parse_args()
    for service in ("control-plane", "subscription", "billing", "delivery", "telemetry"):
        assert (args.bin_dir / service).is_file(), "run make build first"
    name = "caspervpn-onboarding-" + secrets.token_hex(4)
    processes, logs = {}, {}
    fake = FakeTelegram()
    with tempfile.TemporaryDirectory(prefix="caspervpn-onboarding-") as directory:
        temporary = Path(directory)
        try:
            subprocess.run(["docker", "run", "-d", "--name", name, "-e", "POSTGRES_USER=caspervpn",
                            "-e", "POSTGRES_PASSWORD=local-e2e-only", "-e", "POSTGRES_DB=caspervpn",
                            "-p", "127.0.0.1::5432", "postgres:16-alpine"], check=True, stdout=subprocess.DEVNULL)
            pg_port = subprocess.check_output(["docker", "port", name, "5432/tcp"], text=True).strip().rsplit(":", 1)[1]

            def sql(body, database="caspervpn"):
                return subprocess.check_output(["docker", "exec", "-i", name, "psql", "-X", "-At", "-v", "ON_ERROR_STOP=1",
                                                "-U", "caspervpn", "-d", database], input=body.encode()).decode().strip()

            wait_for(lambda: subprocess.run(["docker", "exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "caspervpn", "-d", "caspervpn"],
                                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0, "PostgreSQL")
            for schema in ("billing/internal/store/schema.sql", "subscription/internal/controlplane/schema.sql",
                           "telemetry/internal/store/schema.sql"):
                sql((ROOT / "services" / schema).read_text())
            if args.integration:
                for database in ("cp_integration", "billing_integration", "delivery_integration"):
                    sql('CREATE DATABASE "' + database + '";', "postgres")
                sql((ROOT / "services/billing/internal/store/schema.sql").read_text(), "billing_integration")
                for service, database, variable, target in (
                    ("control-plane", "cp_integration", "TEST_DATABASE_URL", "./..."),
                    ("billing", "billing_integration", "DATABASE_URL", "./..."),
                    ("delivery", "delivery_integration", "TEST_DATABASE_URL", "./internal/botstore/..."),
                ):
                    env = {**os.environ, "REQUIRE_INTEGRATION_DB": "true", variable:
                           f"postgres://caspervpn:local-e2e-only@127.0.0.1:{pg_port}/{database}?sslmode=disable"}
                    subprocess.run(["go", "test", "-race", "-count=1", "-tags", "integration", target],
                                   cwd=ROOT / "services" / service, env=env, check=True)
                print("PASS: PostgreSQL integration suites")
            ports = {name: free_port() for name in ("control-plane", "subscription", "billing", "delivery", "telemetry")}
            urls = {name: f"http://127.0.0.1:{port}" for name, port in ports.items()}
            common = {"ENV": "test", "DATABASE_URL": f"postgres://caspervpn:local-e2e-only@127.0.0.1:{pg_port}/caspervpn?sslmode=disable"}
            configs = {
                "control-plane": {"SEED": "true", "SEED_NODES_FILE": str(ROOT / "services/control-plane/config/seed.nodes.json"),
                                  "CONTROL_PLANE_TOKENS": "admin-test:admin,sub-test:subscription,bill-test:billing,bot-test:delivery,orch-test:orchestrator",
                                  "SUBSCRIPTION_TOKEN_KEY": base64.b64encode(secrets.token_bytes(32)).decode(),
                                  "SUBSCRIPTION_INTERNAL_URL": urls["subscription"], "SUBSCRIPTION_INTERNAL_TOKEN": "internal-test"},
                "subscription": {"CONTROL_PLANE_URL": urls["control-plane"], "CONTROL_PLANE_TOKEN": "sub-test",
                                 "INTERNAL_TOKEN": "internal-test", "SUBSCRIPTION_PUBLIC_BASE_URL": urls["subscription"],
                                 "ROUTING_POLICY_FILE": str(ROOT / "services/subscription/config/routing.ru.json")},
                "billing": {"CONTROL_PLANE_URL": urls["control-plane"], "CONTROL_PLANE_TOKEN": "bill-test",
                            "BILLING_INTERNAL_TOKEN": "billing-private", "BILLING_MOCK_SECRET": "mock-test",
                            "BILLING_PLAN_CATALOG": str(ROOT / "services/billing/config/plans.example.json")},
                "delivery": {"DELIVERY_BOT_ENABLED": "true", "DELIVERY_CONTROL_PLANE_BASE": urls["control-plane"],
                             "DELIVERY_CONTROL_PLANE_TOKEN": "bot-test", "DELIVERY_BILLING_BASE": urls["billing"],
                             "DELIVERY_BILLING_TOKEN": "billing-private", "DELIVERY_TELEGRAM_BASE": fake.url,
                             "DELIVERY_TELEGRAM_TOKEN": "local-test-token", "DELIVERY_PUBLIC_SUBSCRIPTION_BASE": urls["subscription"],
                             "DELIVERY_BOT_DEFAULT_CURRENCY": "BTC", "DELIVERY_BOT_COOLDOWN": "1ms", "DELIVERY_BOT_BURST": "100",
                             "DELIVERY_TELEGRAM_POLL_TIMEOUT": "1s", "DELIVERY_RETRY_DELAY": "50ms"},
                "telemetry": {"TELEMETRY_INTERNAL_TOKEN": "telemetry-test"},
            }

            def start(service):
                log = (temporary / (service + ".log")).open("ab")
                logs[service] = log
                # Do not inherit production secrets or arbitrary service configuration.
                env = {"PATH": os.environ["PATH"], "HOME": str(temporary), **common, **configs[service], "PORT": str(ports[service])}
                processes[service] = subprocess.Popen([str(args.bin_dir.resolve() / service)], env=env, cwd=ROOT, stdout=log, stderr=log)
                wait_for(lambda: request(urls[service] + "/readyz"), service + " readiness")

            for service in configs:
                start(service)
            cp, sub, bill = urls["control-plane"], urls["subscription"], urls["billing"]
            fake.send(1, "/start")
            wait_for(lambda: len(fake.messages) >= 1, "/start reply")
            before = len(fake.messages)
            fake.send(2, "/pay basic BTC")
            text = wait_for(lambda: fake.reply(before, "Invoice "), "/pay invoice")
            invoice_id = re.search(r"Invoice ([^\s]+)", text)[1]
            record = json.loads(sql("SELECT row_to_json(i) FROM invoices i WHERE id='" + invoice_id + "';"))
            webhook = {"external_id": "local-e2e-" + invoice_id, "invoice_id": invoice_id, "status": "settled",
                       "amount": record["amount"], "currency": record["currency"], "confirmations": 6,
                       "ts": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")}
            raw = json.dumps(webhook, separators=(",", ":")).encode()
            signature = hmac.new(b"mock-test", raw, hashlib.sha256).hexdigest()
            request(bill + "/v1/webhooks/mock", webhook, headers={"X-Signature": signature})
            before = len(fake.messages)
            fake.send(3, "/get")
            text = wait_for(lambda: fake.reply(before, sub + "/sub/"), "/get subscription")
            link = re.search(re.escape(sub) + r"/sub/[^\s]+", text)[0]
            profile = request(link + "?format=singbox")
            outbounds = profile["outbounds"]
            assert any(item["type"] == "vless" for item in outbounds)
            assert any(item["type"] == "hysteria2" for item in outbounds)
            user_id = record["anon_user_id"]
            user = request(cp + "/v1/users/" + user_id, token="admin-test")
            state = request(cp + "/v1/subscriptions/" + user["subscription_id"], token="admin-test")
            expiry = state["expires_at"]
            request(bill + "/v1/webhooks/mock", webhook, headers={"X-Signature": signature})
            assert request(cp + "/v1/subscriptions/" + user["subscription_id"], token="admin-test")["expires_at"] == expiry
            print("PASS: Telegram /start /pay /get -> PostgreSQL billing -> personal VLESS+Hy2; webhook replay")

            # All service state must survive restart, including poll cursor and encrypted links.
            for service, process in processes.items():
                process.terminate()
                process.wait(timeout=15)
                logs[service].close()
            for service in configs:
                start(service)
            assert sql("SELECT count(*) FROM invoices;") == "1"
            assert request(link + "?format=singbox")["outbounds"] == outbounds
            before = len(fake.messages)
            fake.send(4, "/get")
            assert link in wait_for(lambda: fake.reply(before, sub + "/sub/"), "stable link after restart")
            assert len([m for m in fake.messages if "Invoice " in m["text"]]) == 1
            print("PASS: restart preserves exact link, credentials, paid term and Telegram dedup cursor")

            # Ignore group messages; command text cannot select another account.
            before = len(fake.messages)
            fake.send(5, "/get", chat_type="group", chat_id=-100)
            fake.send(6, "/get " + user_id, sender=900002)
            wait_for(lambda: any(m["chat_id"] == 900002 for m in fake.messages[before:]), "second user response")
            assert not any(m["chat_id"] == -100 or link in m["text"] for m in fake.messages[before:])
            print("PASS: group privacy and sender-bound account isolation")

            # A missed callback must not let a warmed cached profile bypass a ban.
            request(cp + "/v1/users/" + user_id, {"status": "banned"}, token="admin-test", method="PATCH")
            try:
                request(link + "?format=singbox")
                raise AssertionError("banned user retained subscription access")
            except HTTPError as error:
                assert error.code in (401, 403, 410), error.code
            print("PASS: ban rejects a previously cached subscription")

            # A real custom-format dump and transaction restore, without replacing the source DB.
            dump = subprocess.check_output(["docker", "exec", name, "pg_dump", "-U", "caspervpn", "-d", "caspervpn", "-Fc"])
            sql("CREATE DATABASE restore_check;", "postgres")
            subprocess.run(["docker", "exec", "-i", name, "pg_restore", "-U", "caspervpn", "-d", "restore_check", "--exit-on-error", "--single-transaction"], input=dump, check=True)
            assert sql("SELECT count(*) FROM invoices;", "restore_check") == "1"
            assert sql("SELECT count(*) FROM users;", "restore_check") == sql("SELECT count(*) FROM users;")
            for table in ("subscriptions", "subscription_delivery_tokens", "schedules", "billing_deliveries"):
                # Compare durable entitlement and ciphertext, never print their contents.
                statement = "SELECT row_to_json(t)::text FROM " + table + " t ORDER BY row_to_json(t)::text;"
                assert sql(statement, "restore_check") == sql(statement), "restored state differs: " + table
            for service, process in processes.items():
                process.terminate()
                process.wait(timeout=15)
                logs[service].close()
            common["DATABASE_URL"] = common["DATABASE_URL"].replace("/caspervpn?", "/restore_check?")
            for service in configs:
                start(service)
            request(cp + "/v1/users/" + user_id, {"status": "active"}, token="admin-test", method="PATCH")
            before = len(fake.messages)
            fake.send(7, "/get")
            assert link in wait_for(lambda: fake.reply(before, sub + "/sub/"), "stable link from restored database")
            assert request(link + "?format=singbox")["outbounds"] == outbounds
            print("PASS: restored DB boots all services and recovers exact link, credentials and entitlement")
        except Exception:
            for service in logs:
                print("Service log:", service)
                print((temporary / (service + ".log")).read_text()[-5000:])
            raise
        finally:
            for process in processes.values():
                if process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=15)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
            for log in logs.values():
                log.close()
            fake.server.shutdown()
            # This random test-owned container is the only resource removed.
            subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
