#!/usr/bin/env python3
"""Loopback-only, read-only CasperVPN operator dashboard."""

import argparse
from concurrent.futures import ThreadPoolExecutor, wait
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import ipaddress
import json
import mimetypes
import os
from pathlib import Path
import re
import sys
import time
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


ROOT = Path(__file__).resolve().parents[1]
STATIC_ROOT = ROOT / "web/admin"
CONFIG_ENV_KEYS = (
    "OPERATOR_CONTROL_PLANE_URL", "OPERATOR_SUBSCRIPTION_URL", "OPERATOR_BILLING_URL",
    "OPERATOR_TELEMETRY_URL", "OPERATOR_DELIVERY_URL", "OPERATOR_ORCHESTRATOR_URL",
    "OPERATOR_HTTP_TIMEOUT_SECONDS", "OPERATOR_MAX_RESPONSE_BYTES",
    "OPERATOR_OVERVIEW_DEADLINE_SECONDS",
)
SERVICE_DEFAULTS = {
    "control-plane": "http://127.0.0.1:8081",
    "subscription": "http://127.0.0.1:8082",
    "delivery": "http://127.0.0.1:8083",
    "billing": "http://127.0.0.1:8084",
    "telemetry": "http://127.0.0.1:8085",
}
ACCOUNT_STATES = ("active", "suspended", "expired", "banned")
SUBSCRIPTION_STATES = ("trialing", "active", "past_due", "canceled", "expired")
BILLING_STATES = ("pending", "settled", "expired", "invalid")
SAFE_TEXT = re.compile(r"^[A-Za-z0-9._:+/@ -]{0,160}$")


class OperatorError(Exception):
    pass


class RejectRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def no_redirect_open(request, timeout):
    return build_opener(RejectRedirects).open(request, timeout=timeout)


def utc_now():
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def _bounded_float(value, default, minimum, maximum, name):
    if value in (None, ""):
        return default
    try:
        parsed = float(value)
    except (TypeError, ValueError) as error:
        raise OperatorError(name + " must be a number") from error
    if parsed < minimum or parsed > maximum:
        raise OperatorError(f"{name} must be between {minimum} and {maximum}")
    return parsed


def _bounded_int(value, default, minimum, maximum, name):
    if value in (None, ""):
        return default
    try:
        parsed = int(value)
    except (TypeError, ValueError) as error:
        raise OperatorError(name + " must be an integer") from error
    if parsed < minimum or parsed > maximum:
        raise OperatorError(f"{name} must be between {minimum} and {maximum}")
    return parsed


def _safe_url(value, name):
    parsed = urlsplit(value)
    if parsed.scheme not in ("http", "https") or not parsed.hostname:
        raise OperatorError(name + " must be an HTTP URL")
    if parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ("", "/"):
        raise OperatorError(name + " must not contain credentials, query, fragment or path")
    return value.rstrip("/")


def _safe_text(value, default=""):
    if not isinstance(value, str) or not SAFE_TEXT.fullmatch(value):
        return default
    return value


def _safe_count(value):
    return value if isinstance(value, int) and not isinstance(value, bool) and 0 <= value <= 1_000_000_000 else 0


def _safe_decimal(value):
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        value = str(value)
    if isinstance(value, str) and re.fullmatch(r"[0-9]{1,18}(?:\.[0-9]{1,18})?", value):
        return value
    return ""


class OperatorConfig:
    def __init__(self, values, bind="127.0.0.1", port=8090, timeout=None, max_body=None,
                 overview_deadline=None, demo=False):
        try:
            address = ipaddress.ip_address(bind)
        except ValueError as error:
            raise OperatorError("operator bind must be a loopback IP address") from error
        if not address.is_loopback:
            raise OperatorError("operator bind must be loopback")
        self.bind = bind
        self.port = _bounded_int(port, 8090, 0, 65535, "operator port")
        self.timeout = _bounded_float(
            timeout if timeout is not None else values.get("OPERATOR_HTTP_TIMEOUT_SECONDS"),
            2.0, 0.2, 10.0, "operator HTTP timeout",
        )
        self.max_body = _bounded_int(
            max_body if max_body is not None else values.get("OPERATOR_MAX_RESPONSE_BYTES"),
            1_048_576, 4096, 4_194_304, "operator response limit",
        )
        self.overview_deadline = _bounded_float(
            overview_deadline if overview_deadline is not None else values.get("OPERATOR_OVERVIEW_DEADLINE_SECONDS"),
            5.0, 1.0, 6.0, "operator overview deadline",
        )
        self.demo = demo
        self.urls = {}
        for service, default in SERVICE_DEFAULTS.items():
            key = "OPERATOR_" + service.upper().replace("-", "_") + "_URL"
            self.urls[service] = _safe_url(values.get(key, default), key)
        optional_orchestrator = values.get("OPERATOR_ORCHESTRATOR_URL")
        if optional_orchestrator:
            self.urls["orchestrator"] = _safe_url(optional_orchestrator, "OPERATOR_ORCHESTRATOR_URL")
        self.cp_token = values.get("CP_ADMIN_TOKEN", "")
        self.billing_token = values.get("BILLING_INTERNAL_TOKEN", "")
        if not demo and (not self.cp_token or not self.billing_token):
            raise OperatorError("CP_ADMIN_TOKEN and BILLING_INTERNAL_TOKEN are required")


def project_node(node):
    transports = []
    raw_transports = node.get("transports", []) if isinstance(node, dict) else []
    if isinstance(raw_transports, list):
        for transport in raw_transports[:32]:
            if not isinstance(transport, dict) or not transport.get("enabled"):
                continue
            kind = _safe_text(transport.get("type"))
            if kind and kind not in transports:
                transports.append(kind)
    source = node if isinstance(node, dict) else {}
    return {
        "id": _safe_text(source.get("id")),
        "region": _safe_text(source.get("region")),
        "role": _safe_text(source.get("role")),
        "status": _safe_text(source.get("status")),
        "transports": sorted(transports),
        "created_at": _safe_text(source.get("created_at")),
    }


def project_account_summary(payload):
    payload = payload if isinstance(payload, dict) else {}
    users = payload.get("users", {}) if isinstance(payload.get("users"), dict) else {}
    subscriptions = payload.get("subscriptions", {}) if isinstance(payload.get("subscriptions"), dict) else {}
    recent = []
    raw_recent = payload.get("recent", [])
    if isinstance(raw_recent, list):
        for item in raw_recent[:50]:
            if not isinstance(item, dict):
                continue
            recent.append({
                "id": _safe_text(item.get("id")),
                "status": _safe_text(item.get("status")),
                "subscription_id": _safe_text(item.get("subscription_id")),
                "plan": _safe_text(item.get("plan")),
                "subscription_status": _safe_text(item.get("subscription_status")),
                "expires_at": _safe_text(item.get("expires_at")),
                "grace_until": _safe_text(item.get("grace_until")),
                "updated_at": _safe_text(item.get("updated_at")),
            })
    return {
        "users": {name: _safe_count(users.get(name)) for name in ACCOUNT_STATES},
        "subscriptions": {name: _safe_count(subscriptions.get(name)) for name in SUBSCRIPTION_STATES},
        "recent": recent,
    }


def project_billing_summary(payload):
    payload = payload if isinstance(payload, dict) else {}
    counts = payload.get("counts", {}) if isinstance(payload.get("counts"), dict) else {}
    recent = []
    raw_recent = payload.get("recent", [])
    if isinstance(raw_recent, list):
        for item in raw_recent[:50]:
            if not isinstance(item, dict):
                continue
            recent.append({
                "invoice_id": _safe_text(item.get("invoice_id")),
                "anon_user_id": _safe_text(item.get("anon_user_id")),
                "plan": _safe_text(item.get("plan")),
                "status": _safe_text(item.get("status")),
                "amount": _safe_decimal(item.get("amount")),
                "currency": _safe_text(item.get("currency")),
                "created_at": _safe_text(item.get("created_at")),
                "expires_at": _safe_text(item.get("expires_at")),
            })
    return {"counts": {name: _safe_count(counts.get(name)) for name in BILLING_STATES}, "recent": recent}


def project_plans(payload):
    payload = payload if isinstance(payload, dict) else {}
    items = payload.get("items", [])
    projected = []
    if not isinstance(items, list):
        return projected
    for item in items[:100]:
        if not isinstance(item, dict):
            continue
        prices = item.get("prices", {}) if isinstance(item.get("prices"), dict) else {}
        clean_prices = {}
        for currency, amount in list(prices.items())[:32]:
            currency = _safe_text(currency)
            amount = _safe_decimal(amount)
            if currency and amount:
                clean_prices[currency] = amount
        projected.append({
            "id": _safe_text(item.get("id")),
            "duration_seconds": _safe_count(item.get("duration_seconds")),
            "grace_seconds": _safe_count(item.get("grace_seconds")),
            "prices": clean_prices,
        })
    return projected


class SafeAPIClient:
    def __init__(self, config, opener=no_redirect_open):
        self.config = config
        self.opener = opener

    def _request(self, service, path, token="", expect_json=True, timeout=None):
        headers = {"Accept": "application/json", "User-Agent": "caspervpn-operator/1"}
        if token:
            headers["Authorization"] = "Bearer " + token
        request = Request(self.config.urls[service] + path, headers=headers, method="GET")
        try:
            with self.opener(request, timeout=timeout or self.config.timeout) as response:
                status = getattr(response, "status", 200)
                if status != 200:
                    raise OperatorError("upstream status")
                body = response.read(self.config.max_body + 1)
        except HTTPError as error:
            error.close()
            raise OperatorError("upstream unavailable") from error
        except (URLError, TimeoutError, OSError, ValueError) as error:
            raise OperatorError("upstream unavailable") from error
        if len(body) > self.config.max_body:
            raise OperatorError("upstream response too large")
        if not expect_json:
            return {"ok": True, "status": 200}
        try:
            decoded = json.loads(body)
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            raise OperatorError("upstream returned invalid JSON") from error
        if not isinstance(decoded, dict):
            raise OperatorError("upstream returned invalid JSON")
        return decoded

    def _health(self, service):
        path = "/healthz" if service == "orchestrator" else "/readyz"
        try:
            return self._request(service, path, expect_json=False)
        except OperatorError:
            return {"ok": False, "status": 0}

    def _nodes(self):
        items = []
        cursor = ""
        seen = set()
        deadline = time.monotonic() + self.config.overview_deadline
        for _ in range(10):
            query = {"limit": 1000}
            if cursor:
                query["cursor"] = cursor
            remaining = deadline - time.monotonic()
            if remaining <= 0.05:
                raise OperatorError("fleet deadline exceeded")
            payload = self._request("control-plane", "/v1/nodes?" + urlencode(query), self.config.cp_token,
                                    timeout=min(self.config.timeout, remaining))
            page = payload.get("items", [])
            if not isinstance(page, list):
                raise OperatorError("invalid fleet response")
            items.extend(project_node(node) for node in page[:1000])
            next_cursor = payload.get("next_cursor")
            if not isinstance(next_cursor, str) or not next_cursor:
                return items
            if next_cursor in seen or len(next_cursor) > 512:
                raise OperatorError("invalid fleet pagination")
            seen.add(next_cursor)
            cursor = next_cursor
        raise OperatorError("fleet pagination limit exceeded")

    def overview(self):
        health = {service: {"ok": False, "status": 0} for service in self.config.urls}
        overview = {
            "generated_at": utc_now(), "mode": "live", "health": health,
            "fleet": [],
            "accounts": {name: 0 for name in ACCOUNT_STATES},
            "subscriptions": {name: 0 for name in SUBSCRIPTION_STATES},
            "account_recent": [],
            "billing": {"counts": {name: 0 for name in BILLING_STATES}, "recent": []},
            "plans": [], "errors": {},
        }
        executor = ThreadPoolExecutor(max_workers=min(12, len(self.config.urls) + 4), thread_name_prefix="operator-read")
        futures = {}
        for service in self.config.urls:
            futures["health:" + service] = executor.submit(self._health, service)
        futures["fleet"] = executor.submit(self._nodes)
        futures["accounts"] = executor.submit(
            self._request, "control-plane", "/v1/operator/summary", self.config.cp_token,
        )
        futures["billing"] = executor.submit(
            self._request, "billing", "/v1/operator/summary", self.config.billing_token,
        )
        futures["plans"] = executor.submit(
            self._request, "billing", "/v1/plans", self.config.billing_token,
        )
        done, _pending = wait(futures.values(), timeout=self.config.overview_deadline)
        for name, future in futures.items():
            if future not in done:
                if not name.startswith("health:"):
                    overview["errors"][name] = {
                        "fleet": "Данные флота недоступны",
                        "accounts": "Сводка аккаунтов недоступна",
                        "billing": "Сводка платежей недоступна",
                        "plans": "Каталог тарифов недоступен",
                    }[name]
                continue
            try:
                value = future.result()
                if name.startswith("health:"):
                    overview["health"][name.split(":", 1)[1]] = value
                elif name == "fleet":
                    overview["fleet"] = value
                elif name == "accounts":
                    summary = project_account_summary(value)
                    overview["accounts"] = summary["users"]
                    overview["subscriptions"] = summary["subscriptions"]
                    overview["account_recent"] = summary["recent"]
                elif name == "billing":
                    overview["billing"] = project_billing_summary(value)
                elif name == "plans":
                    overview["plans"] = project_plans(value)
            except OperatorError:
                if not name.startswith("health:"):
                    overview["errors"][name] = {
                        "fleet": "Данные флота недоступны",
                        "accounts": "Сводка аккаунтов недоступна",
                        "billing": "Сводка платежей недоступна",
                        "plans": "Каталог тарифов недоступен",
                    }[name]
        executor.shutdown(wait=False, cancel_futures=True)
        return overview


def demo_overview():
    return {
        "generated_at": utc_now(), "mode": "demo",
        "demo_notice": "Демонстрационные данные. Подключение к сервисам не выполняется.",
        "health": {name: {"ok": True, "status": 200} for name in SERVICE_DEFAULTS},
        "fleet": [
            {"id": "demo-entry", "region": "demo-eu", "role": "entry", "status": "active",
             "transports": ["hysteria2", "vless-reality"], "created_at": utc_now()},
            {"id": "demo-exit", "region": "demo-eu", "role": "exit", "status": "active",
             "transports": [], "created_at": utc_now()},
        ],
        "accounts": {"active": 12, "suspended": 1, "expired": 3, "banned": 0},
        "subscriptions": {"trialing": 2, "active": 10, "past_due": 1, "canceled": 2, "expired": 1},
        "account_recent": [],
        "billing": {"counts": {"pending": 1, "settled": 9, "expired": 2, "invalid": 0}, "recent": []},
        "plans": [{"id": "basic", "duration_seconds": 2_592_000, "grace_seconds": 86_400,
                   "prices": {"BTC": "0.0001"}}],
        "errors": {},
    }


class DemoClient:
    def overview(self):
        return demo_overview()


class OperatorHandler(BaseHTTPRequestHandler):
    server_version = "CasperVPNOperator/1"
    sys_version = ""

    def log_message(self, _format, *_args):
        return

    def _allowed_hosts(self):
        port = self.server.server_address[1]
        return {
            f"127.0.0.1:{port}", f"localhost:{port}", f"[::1]:{port}",
            "127.0.0.1", "localhost", "[::1]",
        }

    def _request_allowed(self):
        host = self.headers.get("Host", "")
        if host not in self._allowed_hosts():
            self._send_json(421, {"error": "unsafe_host"})
            return False
        origin = self.headers.get("Origin")
        if origin and origin not in {"http://" + item for item in self._allowed_hosts()}:
            self._send_json(403, {"error": "unsafe_origin"})
            return False
        return True

    def _security_headers(self, content_type, length, cache="no-store"):
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(length))
        self.send_header("Cache-Control", cache)
        self.send_header("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self'; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("X-Frame-Options", "DENY")
        self.send_header("Referrer-Policy", "no-referrer")
        self.send_header("Cross-Origin-Resource-Policy", "same-origin")

    def _send_json(self, status, payload, include_body=True):
        body = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode()
        self.send_response(status)
        self._security_headers("application/json; charset=utf-8", len(body))
        self.end_headers()
        if include_body:
            self.wfile.write(body)

    def _serve_static(self, include_body=True):
        path = urlsplit(self.path)
        if path.query or path.fragment:
            self._send_json(404, {"error": "not_found"}, include_body)
            return
        name = "index.html" if path.path == "/" else path.path.removeprefix("/")
        if name not in ("index.html", "styles.css", "app.js"):
            self._send_json(404, {"error": "not_found"}, include_body)
            return
        body = (STATIC_ROOT / name).read_bytes()
        content_type = mimetypes.guess_type(name)[0] or "application/octet-stream"
        if content_type.startswith("text/") or content_type == "application/javascript":
            content_type += "; charset=utf-8"
        self.send_response(200)
        self._security_headers(content_type, len(body), "no-cache")
        self.end_headers()
        if include_body:
            self.wfile.write(body)

    def do_GET(self):
        if not self._request_allowed():
            return
        if self.path == "/api/overview":
            self._send_json(200, self.server.operator_client.overview())
            return
        self._serve_static()

    def do_HEAD(self):
        if not self._request_allowed():
            return
        if self.path == "/api/overview":
            self._send_json(200, {}, include_body=False)
            return
        self._serve_static(include_body=False)

    def _read_only(self):
        if not self._request_allowed():
            return
        self._send_json(405, {"error": "read_only"})

    do_POST = _read_only
    do_PUT = _read_only
    do_PATCH = _read_only
    do_DELETE = _read_only
    do_OPTIONS = _read_only


def create_server(config, client=None):
    server = ThreadingHTTPServer((config.bind, config.port), OperatorHandler)
    server.daemon_threads = True
    server.operator_client = client or (DemoClient() if config.demo else SafeAPIClient(config))
    return server


def read_private_env(path):
    spec = importlib.util.spec_from_file_location("casper_launch_for_operator", ROOT / "scripts/launch.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.read_env(path)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env", type=Path, default=ROOT / ".launch/production.env")
    parser.add_argument("--bind", default=os.environ.get("OPERATOR_BIND", "127.0.0.1"))
    parser.add_argument("--port", default=os.environ.get("OPERATOR_PORT", "8090"))
    parser.add_argument("--timeout", default=None)
    parser.add_argument("--max-response-bytes", default=None)
    parser.add_argument("--overview-deadline", default=None)
    parser.add_argument("--demo-fixtures", action="store_true")
    args = parser.parse_args()
    values = {} if args.demo_fixtures else read_private_env(args.env.resolve())
    values.update({key: os.environ[key] for key in CONFIG_ENV_KEYS if key in os.environ})
    config = OperatorConfig(values, bind=args.bind, port=args.port, timeout=args.timeout,
                            max_body=args.max_response_bytes, overview_deadline=args.overview_deadline,
                            demo=args.demo_fixtures)
    server = create_server(config)
    host, port = server.server_address[:2]
    print(f"Operator dashboard listening on http://{host}:{port} ({'demo' if args.demo_fixtures else 'live'} mode)")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    try:
        main()
    except (OSError, OperatorError, ValueError) as error:
        print("Operator startup failed:", str(error), file=sys.stderr)
        sys.exit(1)
