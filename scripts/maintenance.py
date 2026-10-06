#!/usr/bin/env python3
"""Local health transitions and encrypted CasperVPN backup bundles."""

import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import tarfile
import tempfile
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


ROOT = Path(__file__).resolve().parents[1]
OWNED_BACKUP = re.compile(r"^caspervpn-[0-9]{8}T[0-9]{6}Z-[a-f0-9]{8,32}\.bundle\.cms$")
SERVICE_DEFAULTS = {
    "control-plane": "http://127.0.0.1:8081",
    "subscription": "http://127.0.0.1:8082",
    "delivery": "http://127.0.0.1:8083",
    "billing": "http://127.0.0.1:8084",
    "telemetry": "http://127.0.0.1:8085",
}


class MaintenanceError(Exception):
    pass


class RejectRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def no_redirect_open(request, timeout):
    return build_opener(RejectRedirects).open(request, timeout=timeout)


def utc_now():
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def filename_time():
    return datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")


def _private_file(path, label):
    path = Path(path)
    if path.is_symlink():
        raise MaintenanceError(label + " must be a regular file")
    path = path.resolve()
    if not path.is_file():
        raise MaintenanceError(label + " must be a regular file")
    if path.stat().st_mode & 0o077:
        raise MaintenanceError(label + " must have mode 0600")
    return path


def _public_file(path, label):
    path = Path(path)
    if path.is_symlink():
        raise MaintenanceError(label + " must be a regular file")
    path = path.resolve()
    if not path.is_file():
        raise MaintenanceError(label + " must be a regular file")
    return path


def _bounded_int(value, default, minimum, maximum, label):
    if value in (None, ""):
        return default
    try:
        parsed = int(value)
    except (TypeError, ValueError) as error:
        raise MaintenanceError(label + " must be an integer") from error
    if parsed < minimum or parsed > maximum:
        raise MaintenanceError(f"{label} must be between {minimum} and {maximum}")
    return parsed


def _safe_base_url(value, label):
    parsed = urlsplit(value)
    if parsed.scheme not in ("http", "https") or not parsed.hostname:
        raise MaintenanceError(label + " must be an HTTP URL")
    if parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ("", "/"):
        raise MaintenanceError(label + " must not contain credentials, query, fragment or path")
    return value.rstrip("/")


def read_private_env(path):
    spec = importlib.util.spec_from_file_location("casper_launch_for_maintenance", ROOT / "scripts/launch.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.read_env(Path(path).resolve())


def health_endpoints(values):
    endpoints = {}
    for service, default in SERVICE_DEFAULTS.items():
        key = "OPERATOR_" + service.upper().replace("-", "_") + "_URL"
        base = _safe_base_url(values.get(key, os.environ.get(key, default)), key)
        endpoints[service] = base + ("/healthz" if service == "orchestrator" else "/readyz")
    optional_orchestrator = values.get("OPERATOR_ORCHESTRATOR_URL", os.environ.get("OPERATOR_ORCHESTRATOR_URL", ""))
    if optional_orchestrator:
        base = _safe_base_url(optional_orchestrator, "OPERATOR_ORCHESTRATOR_URL")
        endpoints["orchestrator"] = base + "/healthz"
    return endpoints


def check_health(endpoints, timeout=2.0, opener=no_redirect_open):
    try:
        timeout = float(timeout)
    except (TypeError, ValueError) as error:
        raise MaintenanceError("monitor timeout must be a number") from error
    if timeout < 0.2 or timeout > 10.0:
        raise MaintenanceError("monitor timeout must be between 0.2 and 10 seconds")
    result = {}
    for service, url in sorted(endpoints.items()):
        status = 0
        ok = False
        try:
            request = Request(url, headers={"User-Agent": "caspervpn-monitor/1"}, method="GET")
            with opener(request, timeout=timeout) as response:
                status = int(getattr(response, "status", 200))
                response.read(4096)
                ok = status == 200
        except HTTPError as error:
            status = int(error.code)
            error.close()
        except (URLError, TimeoutError, OSError, ValueError):
            status = 0
        result[service] = {"ok": ok, "status": status}
    return result


def _atomic_private_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(path.parent, 0o700)
    temporary = path.with_name("." + path.name + ".tmp-" + secrets.token_hex(6))
    try:
        with os.fdopen(os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as stream:
            json.dump(value, stream, sort_keys=True, separators=(",", ":"))
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        os.chmod(path, 0o600)
    finally:
        if temporary.exists():
            temporary.unlink()


def _append_private_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(path.parent, 0o700)
    descriptor = os.open(path, os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600)
    os.chmod(path, 0o600)
    with os.fdopen(descriptor, "a") as stream:
        stream.write(json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n")
        stream.flush()
        os.fsync(stream.fileno())


def record_health(state_path, journal_path, results, timestamp=None):
    timestamp = timestamp or utc_now()
    state_path = Path(state_path)
    previous = {}
    if state_path.exists():
        try:
            previous_payload = json.loads(state_path.read_text())
            previous = previous_payload.get("services", {}) if isinstance(previous_payload, dict) else {}
        except (OSError, json.JSONDecodeError):
            previous = {}
    clean = {}
    transitions = []
    for service in sorted(results):
        result = results[service] if isinstance(results[service], dict) else {}
        current = {"ok": bool(result.get("ok")), "status": _bounded_int(result.get("status"), 0, 0, 599, "health status")}
        clean[service] = current
        old = previous.get(service) if isinstance(previous.get(service), dict) else None
        if old is None:
            event = "initial"
        elif bool(old.get("ok")) == current["ok"]:
            continue
        elif current["ok"]:
            event = "recovered"
        else:
            event = "down"
        alert = {"at": timestamp, "service": service, "event": event,
                 "ok": current["ok"], "status": current["status"]}
        _append_private_json(journal_path, alert)
        transitions.append(alert)
    _atomic_private_json(state_path, {"updated_at": timestamp, "services": clean})
    return transitions


def validate_external_directory(path):
    path = Path(path)
    if not path.is_absolute():
        raise MaintenanceError("backup destination must be absolute")
    if path.is_symlink():
        raise MaintenanceError("backup destination must be an existing external directory")
    resolved = path.resolve()
    if not resolved.is_dir():
        raise MaintenanceError("backup destination must be an existing external directory")
    repo = ROOT.resolve()
    try:
        resolved.relative_to(repo)
    except ValueError:
        pass
    else:
        raise MaintenanceError("backup destination must be outside the repository")
    if resolved in (Path("/"), Path.home().resolve(), Path.home().resolve().parent):
        raise MaintenanceError("backup destination is too broad")
    return resolved


def validate_include_paths(paths):
    result = []
    broad = {Path("/"), Path.home().resolve(), ROOT.resolve(), ROOT.resolve().parent}
    for raw in paths or ():
        path = Path(raw)
        if not path.is_absolute():
            raise MaintenanceError("included backup paths must be absolute")
        if path.is_symlink():
            raise MaintenanceError("included backup path is missing, linked or too broad")
        resolved = path.resolve()
        if resolved in broad or not resolved.exists():
            raise MaintenanceError("included backup path is missing, linked or too broad")
        if not (resolved.is_file() or resolved.is_dir()):
            raise MaintenanceError("included backup path must be a file or directory")
        result.append(resolved)
    return result


def _archive_add_safe(archive, path, arcname, remaining):
    if path.is_symlink():
        raise MaintenanceError("backup include contains a symbolic link")
    if path.is_file():
        size = path.stat().st_size
        if size > remaining[0]:
            raise MaintenanceError("backup bundle exceeds configured size limit")
        remaining[0] -= size
        archive.add(path, arcname=arcname, recursive=False)
        return
    if not path.is_dir():
        raise MaintenanceError("backup include contains a special file")
    archive.add(path, arcname=arcname, recursive=False)
    for child in sorted(path.iterdir(), key=lambda item: item.name):
        _archive_add_safe(archive, child, arcname + "/" + child.name, remaining)


def _build_bundle(bundle, dump, env_path, include_paths, max_bytes):
    remaining = [max_bytes]
    with tarfile.open(bundle, "w") as archive:
        _archive_add_safe(archive, dump, "database.dump", remaining)
        _archive_add_safe(archive, env_path, "production.env", remaining)
        for index, path in enumerate(include_paths, start=1):
            safe_name = re.sub(r"[^A-Za-z0-9._-]", "_", path.name) or "item"
            _archive_add_safe(archive, path, f"extras/{index:02d}-{safe_name}", remaining)
    os.chmod(bundle, 0o600)


def _run_checked(runner, command):
    try:
        runner(command, check=True, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    except (OSError, subprocess.CalledProcessError) as error:
        raise MaintenanceError("maintenance subprocess failed") from error


def _openssl_encrypt(source, output, certificate, runner):
    _run_checked(runner, [
        shutil.which("openssl") or "openssl", "cms", "-encrypt", "-binary", "-aes-256-gcm",
        "-in", str(source), "-out", str(output), "-outform", "DER", str(certificate),
    ])


def _openssl_decrypt(source, output, certificate, private_key, runner):
    _run_checked(runner, [
        shutil.which("openssl") or "openssl", "cms", "-decrypt", "-binary", "-inform", "DER",
        "-in", str(source), "-recip", str(certificate), "-inkey", str(private_key), "-out", str(output),
    ])


def _extract_dump(bundle, output):
    try:
        with tarfile.open(bundle, "r") as archive:
            members = archive.getmembers()
            for member in members:
                parts = Path(member.name).parts
                if member.name.startswith("/") or ".." in parts or member.issym() or member.islnk():
                    raise MaintenanceError("backup bundle contains an unsafe archive entry")
            matches = [member for member in members if member.name == "database.dump" and member.isfile()]
            env_matches = [member for member in members if member.name == "production.env" and member.isfile()]
            if len(matches) != 1 or len(env_matches) != 1:
                raise MaintenanceError("backup bundle is missing database or environment data")
            source = archive.extractfile(matches[0])
            if source is None:
                raise MaintenanceError("backup bundle database is unreadable")
            with os.fdopen(os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "wb") as target:
                shutil.copyfileobj(source, target)
    except (OSError, tarfile.TarError) as error:
        raise MaintenanceError("backup bundle is invalid") from error


def _launch_command(env_path, action, target):
    return [sys.executable, str(ROOT / "scripts/launch.py"), "--env", str(env_path), action, str(target)]


def _restore_from_ciphertext(env_path, encrypted, certificate, private_key, runner, temporary_dir):
    clear_bundle = temporary_dir / "restore.bundle.tar"
    restored_dump = temporary_dir / "restore.dump"
    _openssl_decrypt(encrypted, clear_bundle, certificate, private_key, runner)
    os.chmod(clear_bundle, 0o600)
    _extract_dump(clear_bundle, restored_dump)
    _run_checked(runner, _launch_command(env_path, "restore-check", restored_dump) + ["--cleanup"])


@contextmanager
def operation_lock(path):
    path = Path(path)
    descriptor = os.open(path, os.O_RDWR | os.O_CREAT, 0o600)
    try:
        os.chmod(path, 0o600)
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise MaintenanceError("another maintenance operation is running") from error
        yield
    finally:
        fcntl.flock(descriptor, fcntl.LOCK_UN)
        os.close(descriptor)


def _prune_owned(destination, keep):
    candidates = []
    for path in destination.iterdir():
        if path.is_file() and not path.is_symlink() and OWNED_BACKUP.fullmatch(path.name):
            candidates.append(path)
    for path in sorted(candidates, key=lambda item: item.name, reverse=True)[keep:]:
        path.unlink()


def create_backup(env_path, destination, certificate, private_key, keep=14, runner=subprocess.run,
                  include_paths=(), max_bytes=10 * 1024 * 1024 * 1024, now=None, nonce=None):
    env_path = _private_file(env_path, "production env")
    destination = validate_external_directory(destination)
    certificate = _public_file(certificate, "backup recipient certificate")
    private_key = _private_file(private_key, "backup recipient private key")
    include_paths = validate_include_paths(include_paths)
    keep = _bounded_int(keep, 14, 1, 3650, "backup retention")
    max_bytes = _bounded_int(max_bytes, 10 * 1024 * 1024 * 1024, 1024 * 1024, 100 * 1024 * 1024 * 1024,
                             "backup bundle size")
    now = now or filename_time()
    nonce = nonce or secrets.token_hex(6)
    if not re.fullmatch(r"[0-9]{8}T[0-9]{6}Z", now) or not re.fullmatch(r"[a-f0-9]{8,32}", nonce):
        raise MaintenanceError("invalid backup name components")
    final = destination / f"caspervpn-{now}-{nonce}.bundle.cms"
    if final.exists():
        raise MaintenanceError("backup output already exists")
    state_dir = ROOT / ".launch/maintenance"
    state_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(state_dir, 0o700)
    with operation_lock(destination / ".caspervpn-maintenance.lock"):
        with tempfile.TemporaryDirectory(prefix="backup-", dir=state_dir) as temporary_name:
            temporary_dir = Path(temporary_name)
            os.chmod(temporary_dir, 0o700)
            dump = temporary_dir / "database.dump"
            bundle = temporary_dir / "bundle.tar"
            partial = destination / (final.name + ".partial")
            try:
                _run_checked(runner, _launch_command(env_path, "backup", dump))
                _build_bundle(bundle, dump, env_path, include_paths, max_bytes)
                with os.fdopen(os.open(partial, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "wb"):
                    pass
                _openssl_encrypt(bundle, partial, certificate, runner)
                os.chmod(partial, 0o600)
                _restore_from_ciphertext(env_path, partial, certificate, private_key, runner, temporary_dir)
                os.replace(partial, final)
                os.chmod(final, 0o600)
                _prune_owned(destination, keep)
            except Exception as error:
                if partial.exists():
                    partial.unlink()
                if isinstance(error, MaintenanceError):
                    raise
                raise MaintenanceError("backup operation failed") from error
    return final


def restore_encrypted_backup(env_path, destination, encrypted, certificate, private_key,
                             runner=subprocess.run):
    env_path = _private_file(env_path, "production env")
    destination = validate_external_directory(destination)
    certificate = _public_file(certificate, "backup recipient certificate")
    private_key = _private_file(private_key, "backup recipient private key")
    encrypted = Path(encrypted)
    if encrypted.is_symlink():
        raise MaintenanceError("backup filename or file type is invalid")
    encrypted = encrypted.resolve()
    try:
        encrypted.relative_to(destination)
    except ValueError as error:
        raise MaintenanceError("backup must be inside the configured destination") from error
    if not encrypted.is_file() or not OWNED_BACKUP.fullmatch(encrypted.name):
        raise MaintenanceError("backup filename or file type is invalid")
    state_dir = ROOT / ".launch/maintenance"
    state_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(state_dir, 0o700)
    with operation_lock(destination / ".caspervpn-maintenance.lock"):
        with tempfile.TemporaryDirectory(prefix="restore-", dir=state_dir) as temporary_name:
            temporary_dir = Path(temporary_name)
            os.chmod(temporary_dir, 0o700)
            _restore_from_ciphertext(env_path, encrypted, certificate, private_key, runner, temporary_dir)


def _required_path(value, label):
    if not value:
        raise MaintenanceError(label + " is required")
    return Path(value)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env", type=Path, default=ROOT / ".launch/production.env")
    subparsers = parser.add_subparsers(dest="action", required=True)
    monitor = subparsers.add_parser("monitor")
    monitor.add_argument("--state", type=Path, default=Path(os.environ.get("MONITOR_STATE_FILE", ROOT / ".launch/monitor/state.json")))
    monitor.add_argument("--journal", type=Path, default=Path(os.environ.get("MONITOR_ALERT_JOURNAL", ROOT / ".launch/monitor/alerts.jsonl")))
    monitor.add_argument("--timeout", default=os.environ.get("MONITOR_HTTP_TIMEOUT_SECONDS", "2"))
    backup = subparsers.add_parser("backup")
    backup.add_argument("--destination", type=Path, default=os.environ.get("BACKUP_DESTINATION"))
    backup.add_argument("--recipient-cert", type=Path, default=os.environ.get("BACKUP_RECIPIENT_CERT"))
    backup.add_argument("--recipient-key", type=Path, default=os.environ.get("BACKUP_RECIPIENT_KEY"))
    backup.add_argument("--include", action="append", default=[])
    backup.add_argument("--keep", default=os.environ.get("BACKUP_RETENTION_COUNT", "14"))
    backup.add_argument("--max-bundle-bytes", default=os.environ.get("BACKUP_MAX_BUNDLE_BYTES", str(10 * 1024 * 1024 * 1024)))
    restore = subparsers.add_parser("restore-check")
    restore.add_argument("backup", type=Path)
    restore.add_argument("--destination", type=Path, default=os.environ.get("BACKUP_DESTINATION"))
    restore.add_argument("--recipient-cert", type=Path, default=os.environ.get("BACKUP_RECIPIENT_CERT"))
    restore.add_argument("--recipient-key", type=Path, default=os.environ.get("BACKUP_RECIPIENT_KEY"))
    args = parser.parse_args()
    values = read_private_env(args.env)
    if args.action == "monitor":
        args.state.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        os.chmod(args.state.parent, 0o700)
        with operation_lock(args.state.parent / ".monitor.lock"):
            results = check_health(health_endpoints(values), timeout=args.timeout)
            transitions = record_health(args.state, args.journal, results)
        unhealthy = [name for name, result in results.items() if not result["ok"]]
        print(f"Health checked: {len(results)} services, {len(transitions)} transitions, {len(unhealthy)} unhealthy.")
        if unhealthy:
            raise MaintenanceError("one or more services are unhealthy")
    elif args.action == "backup":
        configured = os.environ.get("BACKUP_INCLUDE_PATHS", "")
        include_paths = list(args.include) + [item for item in configured.split(os.pathsep) if item]
        output = create_backup(
            args.env, _required_path(args.destination, "backup destination"),
            _required_path(args.recipient_cert, "backup recipient certificate"),
            _required_path(args.recipient_key, "backup recipient private key"),
            keep=args.keep, include_paths=include_paths, max_bytes=args.max_bundle_bytes,
        )
        print("Encrypted backup verified and created:", output)
    elif args.action == "restore-check":
        restore_encrypted_backup(
            args.env, _required_path(args.destination, "backup destination"), args.backup,
            _required_path(args.recipient_cert, "backup recipient certificate"),
            _required_path(args.recipient_key, "backup recipient private key"),
        )
        print("Encrypted backup passed isolated restore-check.")


if __name__ == "__main__":
    try:
        main()
    except (OSError, MaintenanceError, ValueError) as error:
        print("Maintenance operation failed:", str(error), file=sys.stderr)
        sys.exit(1)
