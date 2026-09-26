#!/usr/bin/env python3
"""Explicit launch operations. No cloud provisioning, secret logging or auto-destroy."""
import argparse
import base64
import json
import ipaddress
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import time
from urllib.parse import urlsplit
from urllib.error import HTTPError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
SERVICES = ("control-plane", "subscription", "billing", "telemetry", "delivery")
PORTS = dict(zip(SERVICES, (8081, 8082, 8084, 8085, 8083)))
ROLES = ("ADMIN", "ORCHESTRATOR", "TELEMETRY", "SUBSCRIPTION", "BILLING", "DELIVERY")


def read_env(path):
    if path.stat().st_mode & 0o077:
        raise ValueError("env file must have mode 0600")
    result = {}
    for line in path.read_text().splitlines():
        if not line or line.startswith("#"):
            continue
        key, sep, value = line.partition("=")
        if not sep or not re.fullmatch(r"[A-Z][A-Z0-9_]*", key):
            raise ValueError("invalid env syntax; use KEY=value without shell expansion")
        if key in result:
            raise ValueError("duplicate env key: " + key)
        if any(c in value for c in ("'", '"', "$", "`", "\r")):
            raise ValueError("unsupported env quoting/expansion: " + key)
        result[key] = value
    return result


def initialize(path):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    values = {
        "COMPOSE_PROJECT_NAME": "caspervpn-production",
        "SERVICE_SUBNET": f"172.{20 + secrets.randbelow(10)}.{secrets.randbelow(254)}.0/24",
        "POSTGRES_PASSWORD": secrets.token_hex(32),
        **{"CP_" + role + "_TOKEN": secrets.token_hex(32) for role in ROLES},
        "SUBSCRIPTION_INTERNAL_TOKEN": secrets.token_hex(32),
        "BILLING_INTERNAL_TOKEN": secrets.token_hex(32),
        "TELEMETRY_INTERNAL_TOKEN": secrets.token_hex(32),
        **{key: base64.b64encode(secrets.token_bytes(32)).decode() for key in
           ("SUBSCRIPTION_TOKEN_KEY", "DELIVERY_SIGN_SEED", "DELIVERY_SEAL_KEY")},
        "PUBLIC_HOST": "",
        "TLS_EMAIL": "",
        "DELIVERY_TELEGRAM_TOKEN": "",
        "BTCPAY_BASE_URL": "",
        "BTCPAY_API_KEY": "",
        "BTCPAY_STORE_ID": "",
        "BTCPAY_WEBHOOK_SECRET": "",
        "BTCPAY_CURRENCIES": "BTC",
        "DELIVERY_BOT_DEFAULT_CURRENCY": "BTC",
        "BILLING_PLAN_CATALOG_HOST": str(path.parent / "plans.json"),
        "ROUTING_POLICY_HOST": str(ROOT / "services/subscription/config/routing.ru.json"),
    }
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as stream:
        stream.write("# Generated locally. Store an encrypted off-host copy with DB backups.\n")
        stream.writelines(f"{key}={value}\n" for key, value in values.items())
    print("Created private settings:", path)
    print("External account/domain fields and an approved pricing catalog remain required.")


def validate(values):
    missing = [key for key, value in values.items() if not value]
    required = ["POSTGRES_PASSWORD", "PUBLIC_HOST", "TLS_EMAIL", "DELIVERY_TELEGRAM_TOKEN", "SERVICE_SUBNET",
                "BTCPAY_BASE_URL", "BTCPAY_API_KEY", "BTCPAY_STORE_ID", "BTCPAY_WEBHOOK_SECRET",
                "BTCPAY_CURRENCIES", "DELIVERY_BOT_DEFAULT_CURRENCY", "BILLING_PLAN_CATALOG_HOST",
                "ROUTING_POLICY_HOST", "SUBSCRIPTION_TOKEN_KEY", "DELIVERY_SIGN_SEED", "DELIVERY_SEAL_KEY",
                "SUBSCRIPTION_INTERNAL_TOKEN", "BILLING_INTERNAL_TOKEN", "TELEMETRY_INTERNAL_TOKEN"]
    required += ["CP_" + role + "_TOKEN" for role in ROLES]
    missing += [key for key in required if key not in values]
    if missing:
        raise ValueError("missing settings: " + ", ".join(sorted(set(missing))))
    subnet = ipaddress.ip_network(values["SERVICE_SUBNET"], strict=True)
    if subnet.version != 4 or not subnet.is_private or subnet.prefixlen != 24:
        raise ValueError("SERVICE_SUBNET must be a private IPv4 /24 for this stack only")
    tokens = [values[key] for key in required if key.endswith("_TOKEN") and key != "DELIVERY_TELEGRAM_TOKEN"]
    if any(len(token) < 32 or token.startswith("dev-") for token in tokens) or len(tokens) != len(set(tokens)):
        raise ValueError("internal tokens must be distinct, random and at least 32 characters")
    if not re.fullmatch(r"[a-fA-F0-9]{32,}", values["POSTGRES_PASSWORD"]):
        raise ValueError("POSTGRES_PASSWORD must be a URL-safe generated hex secret")
    for key in ("SUBSCRIPTION_TOKEN_KEY", "DELIVERY_SIGN_SEED", "DELIVERY_SEAL_KEY"):
        if len(base64.b64decode(values[key], validate=True)) != 32:
            raise ValueError(key + " must encode 32 bytes")
    host = values["PUBLIC_HOST"]
    if not re.fullmatch(r"[a-zA-Z0-9](?:[a-zA-Z0-9.-]*[a-zA-Z0-9])?", host) or "." not in host:
        raise ValueError("PUBLIC_HOST must be a DNS hostname without a scheme, port or path")
    if host.endswith((".test", ".example", ".invalid", ".localhost")) or host in ("example.com", "example.org", "example.net"):
        raise ValueError("PUBLIC_HOST must be the real launch hostname")
    url = urlsplit(values["BTCPAY_BASE_URL"])
    if url.scheme != "https" or not url.hostname or url.username or url.password or url.query or url.fragment:
        raise ValueError("BTCPAY_BASE_URL must be an HTTPS URL without credentials, query or fragment")
    if "BILLING_MOCK_SECRET" in values:
        raise ValueError("mock payments are forbidden in production")
    for key in ("BILLING_PLAN_CATALOG_HOST", "ROUTING_POLICY_HOST"):
        if not Path(values[key]).is_absolute() or not Path(values[key]).is_file():
            raise ValueError(key + " must reference an existing absolute file")
        json.loads(Path(values[key]).read_text())
    currencies = set(values["BTCPAY_CURRENCIES"].split(","))
    currency = values["DELIVERY_BOT_DEFAULT_CURRENCY"]
    plans = json.loads(Path(values["BILLING_PLAN_CATALOG_HOST"]).read_text())["plans"]
    if currency not in currencies or not any(p["id"] == "basic" and currency in p["prices"] for p in plans):
        raise ValueError("bot default currency must be supported by BTCPay and basic plan")


class Stack:
    def __init__(self, path, values):
        self.values = values
        if subprocess.run(["docker", "compose", "version"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
            compose = ["docker", "compose"]
        elif shutil.which("docker-compose"):
            compose = ["docker-compose"]
        else:
            raise ValueError("Docker Compose v2 is required")
        self.command = compose + ["--env-file", str(path), "-f", str(ROOT / "docker-compose.production.yml")]
        # Prevent unrelated inherited COMPOSE/credential variables overriding the private file.
        self.env = {k: v for k, v in os.environ.items() if k not in values and not k.startswith("COMPOSE_")}

    def run(self, *args, **kwargs):
        return subprocess.run(self.command + list(args), cwd=ROOT, env=self.env, check=True, **kwargs)

    def sql(self, body, database="caspervpn"):
        return self.run("exec", "-T", "postgres", "psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-U", "caspervpn", "-d", database,
                        input=body.encode(), stdout=subprocess.PIPE)

    def migrate(self):
        # CP and delivery have advisory-locked embedded migrations at startup.
        paths = ("services/billing/internal/store/schema.sql", "services/subscription/internal/controlplane/schema.sql",
                 "services/telemetry/internal/store/schema.sql")
        body = "BEGIN;\nSELECT pg_advisory_xact_lock(4771123399);\n"
        body += "\n".join((ROOT / path).read_text() for path in paths)
        self.sql(body + "\nCOMMIT;\n")
        print("Applied external schemas atomically.")

    def ready(self, services=SERVICES):
        deadline = time.monotonic() + 90
        pending = set(services)
        while pending and time.monotonic() < deadline:
            for service in tuple(pending):
                try:
                    with urlopen(f"http://127.0.0.1:{PORTS[service]}/readyz", timeout=2) as response:
                        if response.status == 200:
                            pending.remove(service)
                except (OSError, ValueError):
                    pass
            if pending:
                time.sleep(1)
        if pending:
            raise ValueError("services not ready: " + ", ".join(sorted(pending)))
        print("Requested APIs ready. Fleet acceptance is a separate release gate.")

    def fleet_ready(self):
        req = Request("http://127.0.0.1:8081/v1/nodes?status=active&limit=1000",
                      headers={"Authorization": "Bearer " + self.values["CP_ADMIN_TOKEN"]})
        with urlopen(req, timeout=5) as response:
            nodes = json.load(response)["items"]
        transports = {t["type"] for node in nodes if node["role"] in ("entry", "combined")
                      and node["status"] == "active" for t in node["transports"] if t["enabled"]}
        if not {"vless-reality", "hysteria2"}.issubset(transports):
            raise ValueError("publish requires active entries with personal VLESS and Hysteria2")

    def edge_ready(self, timeout=90):
        # Default certificate verification and public DNS resolution are deliberate.
        # Check the application's exact denial response, not an arbitrary web 404.
        url = "https://" + self.values["PUBLIC_HOST"] + "/sub/launch-readiness-invalid-token"
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                with urlopen(url, timeout=5):
                    pass
            except HTTPError as error:
                if error.code == 401:
                    try:
                        if json.loads(error.read(4096)) == {"error": "unknown or revoked token"}:
                            print("Public HTTPS reaches the subscription API with a valid certificate.")
                            return
                    except ValueError:
                        pass
            except OSError:
                pass
            time.sleep(1)
        raise ValueError("public DNS/TLS/subscription route is not ready")

    def publish(self):
        self.run("stop", "delivery")
        self.fleet_ready()
        self.ready(tuple(service for service in SERVICES if service != "delivery"))
        self.run("up", "-d", "edge")
        self.edge_ready()
        self.run("up", "-d", "delivery")
        self.ready()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env", type=Path, default=ROOT / ".launch/production.env")
    sub = parser.add_subparsers(dest="action", required=True)
    for name in ("init", "preflight", "build", "migrate", "up", "publish", "status", "pause-sales", "stop"):
        sub.add_parser(name)
    backup = sub.add_parser("backup")
    backup.add_argument("output", type=Path)
    restore = sub.add_parser("restore-check")
    restore.add_argument("backup", type=Path)
    args = parser.parse_args()
    path = args.env.resolve()
    if args.action == "init":
        initialize(path)
        return
    values = read_env(path)
    validate(values)
    if not shutil.which("docker"):
        raise ValueError("Docker Compose is required")
    stack = Stack(path, values)
    stack.run("config", "-q")
    if args.action == "preflight":
        print("Local production configuration valid. No external accounts, DNS or payments were contacted.")
    elif args.action == "build":
        stack.run("build", "--pull", *SERVICES)
    elif args.action == "migrate":
        stack.migrate()
    elif args.action == "up":
        stack.run("up", "-d", "--wait", "postgres")
        stack.migrate()
        private = tuple(service for service in SERVICES if service != "delivery")
        stack.run("up", "-d", *private)
        stack.ready(private)
        print("Private APIs started. Complete fleet acceptance before publish enables the bot and HTTPS edge.")
    elif args.action == "publish":
        stack.publish()
    elif args.action == "status":
        stack.run("ps")
        stack.ready()
        stack.edge_ready(timeout=5)
    elif args.action == "pause-sales":
        stack.run("stop", "edge", "delivery")
        print("Public edge and Telegram stopped; database and private APIs retained.")
    elif args.action == "stop":
        stack.run("stop")
        print("Stack stopped. Volumes and database retained.")
    elif args.action == "backup":
        args.output.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        with os.fdopen(os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "wb") as stream:
            stack.run("exec", "-T", "postgres", "pg_dump", "-U", "caspervpn", "-d", "caspervpn", "-Fc", stdout=stream)
        print("Database backup created. Preserve the matching private keys separately.")
    elif args.action == "restore-check":
        # Never overwrite the application DB. Leave the isolated restore available for inspection.
        database = "restore_check_" + secrets.token_hex(6)
        stack.sql(f'CREATE DATABASE "{database}";', database="postgres")
        with args.backup.open("rb") as stream:
            stack.run("exec", "-T", "postgres", "pg_restore", "--exit-on-error", "--no-owner", "--single-transaction",
                      "-U", "caspervpn", "-d", database, stdin=stream)
        stack.sql("SELECT count(*) FROM users; SELECT count(*) FROM invoices; SELECT count(*) FROM subscription_tokens;", database)
        print("Restored successfully into isolated database:", database)
        print("This check leaves that database intact; production data was not replaced.")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as error:
        # Commands contain no secrets; malformed config never prints a value.
        print("Launch operation failed:", str(error), file=sys.stderr)
        sys.exit(1)
