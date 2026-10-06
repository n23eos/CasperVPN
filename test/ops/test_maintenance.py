import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("casper_maintenance", ROOT / "scripts/maintenance.py")
maintenance = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(maintenance)


class MonitorTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.state = Path(self.temp.name) / "state.json"
        self.journal = Path(self.temp.name) / "alerts.jsonl"

    def alerts(self):
        if not self.journal.exists():
            return []
        return [json.loads(line) for line in self.journal.read_text().splitlines()]

    def test_first_change_recovery_and_no_repeat(self):
        first = {"billing": {"ok": True, "status": 200}, "control-plane": {"ok": True, "status": 200}}
        maintenance.record_health(self.state, self.journal, first, "2026-10-06T00:00:00Z")
        self.assertEqual([item["event"] for item in self.alerts()], ["initial", "initial"])

        maintenance.record_health(self.state, self.journal, first, "2026-10-06T00:01:00Z")
        self.assertEqual(len(self.alerts()), 2)

        failed = {**first, "billing": {"ok": False, "status": 503}}
        maintenance.record_health(self.state, self.journal, failed, "2026-10-06T00:02:00Z")
        self.assertEqual(self.alerts()[-1]["event"], "down")
        maintenance.record_health(self.state, self.journal, failed, "2026-10-06T00:03:00Z")
        self.assertEqual(len(self.alerts()), 3)

        maintenance.record_health(self.state, self.journal, first, "2026-10-06T00:04:00Z")
        self.assertEqual(self.alerts()[-1]["event"], "recovered")
        self.assertEqual(self.state.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.journal.stat().st_mode & 0o777, 0o600)


class BackupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        base = Path(self.temp.name)
        self.destination = base / "external"
        self.destination.mkdir(mode=0o700)
        self.certificate = base / "recipient.pem"
        self.certificate.write_text("test certificate")
        self.private_key = base / "recipient-key.pem"
        self.private_key.write_text("test private key")
        os.chmod(self.private_key, 0o600)
        self.env = base / "production.env"
        self.env.write_text("PLACEHOLDER=value\n")
        os.chmod(self.env, 0o600)

    def runner(self, fail_stage=None):
        calls = []

        def run(command, **kwargs):
            calls.append(command)
            if command[0].endswith("python") or "python" in Path(command[0]).name:
                action = "backup" if "backup" in command else "restore-check" if "restore-check" in command else ""
                if action == "backup":
                    target = Path(command[command.index("backup") + 1])
                    target.write_bytes(b"raw-dump")
                elif action == "restore-check" and fail_stage == "restore":
                    raise subprocess.CalledProcessError(1, command)
            elif "openssl" in Path(command[0]).name:
                decrypt = "-decrypt" in command
                if not decrypt and fail_stage == "encrypt":
                    raise subprocess.CalledProcessError(1, command)
                if decrypt and fail_stage == "decrypt":
                    raise subprocess.CalledProcessError(1, command)
                output = Path(command[command.index("-out") + 1])
                if decrypt:
                    source = Path(command[command.index("-in") + 1])
                    output.write_bytes(source.with_suffix(".test-clear").read_bytes())
                else:
                    source = Path(command[command.index("-in") + 1])
                    output.write_bytes(b"encrypted")
                    output.with_suffix(".test-clear").write_bytes(source.read_bytes())
            return subprocess.CompletedProcess(command, 0)

        return run, calls

    def test_success_enforces_retention_only_for_owned_names(self):
        for name in ("caspervpn-20261001T000000Z-aabbccdd.bundle.cms", "caspervpn-20261002T000000Z-aabbccdd.bundle.cms"):
            (self.destination / name).write_bytes(b"old")
        unrelated = self.destination / "someone-else.dump.enc"
        unrelated.write_bytes(b"keep")
        run, calls = self.runner()
        output = maintenance.create_backup(
            self.env, self.destination, self.certificate, self.private_key, keep=2, runner=run,
            now="20261006T000000Z", nonce="abcdef12",
        )
        self.assertEqual(output.name, "caspervpn-20261006T000000Z-abcdef12.bundle.cms")
        self.assertTrue(output.exists())
        self.assertTrue(unrelated.exists())
        owned = sorted(self.destination.glob("caspervpn-*.bundle.cms"))
        self.assertEqual(len(owned), 2)
        self.assertTrue(any("restore-check" in command for call in calls for command in call))
        restore_call = next(call for call in calls if "restore-check" in call)
        self.assertIn("--cleanup", restore_call)

    def test_encryption_failure_keeps_existing_backups_and_no_partial(self):
        old = self.destination / "caspervpn-20261001T000000Z-aabbccdd.bundle.cms"
        old.write_bytes(b"old")
        run, _ = self.runner(fail_stage="encrypt")
        with self.assertRaises(maintenance.MaintenanceError):
            maintenance.create_backup(self.env, self.destination, self.certificate, self.private_key, 1, run,
                                      now="20261006T000000Z", nonce="abcdef12")
        self.assertEqual(old.read_bytes(), b"old")
        self.assertEqual(list(self.destination.glob("*.partial")), [])

    def test_restore_failure_does_not_publish_or_prune_backup(self):
        old = self.destination / "caspervpn-20261001T000000Z-aabbccdd.bundle.cms"
        old.write_bytes(b"old")
        run, _ = self.runner(fail_stage="restore")
        with self.assertRaises(maintenance.MaintenanceError):
            maintenance.create_backup(self.env, self.destination, self.certificate, self.private_key, 1, run,
                                      now="20261006T000000Z", nonce="abcdef12")
        self.assertTrue(old.exists())
        self.assertFalse((self.destination / "caspervpn-20261006T000000Z-abcdef12.bundle.cms").exists())

    def test_tampered_ciphertext_fails_before_restore(self):
        backup = self.destination / "caspervpn-20261001T000000Z-aabbccdd.bundle.cms"
        backup.write_bytes(b"tampered")
        run, calls = self.runner(fail_stage="decrypt")
        with self.assertRaises(maintenance.MaintenanceError):
            maintenance.restore_encrypted_backup(
                self.env, self.destination, backup, self.certificate, self.private_key, run,
            )
        self.assertFalse(any("restore-check" in part for call in calls for part in call))

    @unittest.skipUnless(shutil.which("openssl"), "openssl is required")
    def test_real_cms_gcm_rejects_tampering(self):
        clear = Path(self.temp.name) / "clear.tar"
        encrypted = Path(self.temp.name) / "encrypted.cms"
        decrypted = Path(self.temp.name) / "decrypted.tar"
        clear.write_bytes(b"authenticated backup content")
        subprocess.run([
            shutil.which("openssl"), "req", "-x509", "-newkey", "rsa:2048", "-sha256", "-days", "1", "-nodes",
            "-subj", "/CN=CasperVPN test backup", "-keyout", str(self.private_key), "-out", str(self.certificate),
        ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        maintenance._openssl_encrypt(clear, encrypted, self.certificate, subprocess.run)
        body = bytearray(encrypted.read_bytes())
        body[-1] ^= 1
        encrypted.write_bytes(body)
        with self.assertRaises(maintenance.MaintenanceError):
            maintenance._openssl_decrypt(encrypted, decrypted, self.certificate, self.private_key, subprocess.run)

    def test_destination_and_restore_paths_are_bounded(self):
        with self.assertRaises(maintenance.MaintenanceError):
            maintenance.validate_external_directory(ROOT / "backups")
        outside = Path(self.temp.name) / "outside.dump.enc"
        outside.write_bytes(b"encrypted")
        run, _ = self.runner()
        with self.assertRaises(maintenance.MaintenanceError):
            maintenance.restore_encrypted_backup(
                self.env, self.destination, outside, self.certificate, self.private_key, run,
            )
        linked_destination = Path(self.temp.name) / "linked-destination"
        linked_destination.symlink_to(self.destination, target_is_directory=True)
        with self.assertRaises(maintenance.MaintenanceError):
            maintenance.validate_external_directory(linked_destination)


if __name__ == "__main__":
    unittest.main()
