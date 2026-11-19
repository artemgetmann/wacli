"""Exercise packaged agent startup against a disposable, credential-free store."""

import fcntl
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def main():
    binary = Path(sys.argv[1] if len(sys.argv) > 1 else "dist/wacli").resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix="wacli-owner-") as tmp:
        root = Path(tmp)
        store = root / "store"
        store.mkdir(mode=0o700)
        lock_path = store / "LOCK"
        record_path = store / "openclaw-sync-owner.json"
        log_path = store / "openclaw-sync.log"
        env = dict(os.environ, XDG_CACHE_HOME=str(root / "cache"), HOME=str(root))
        env.pop("WACLI_READONLY", None)

        def ensure():
            result = subprocess.run(
                [binary, "--store", store, "agent", "live", "ensure",
                 "--json", "--settle-ms", "1000"],
                env=env, capture_output=True, text=True, timeout=20, check=True,
            )
            return json.loads(result.stdout)

        def contend(previous_record):
            # Hold the real kernel lock with LOCK text still empty, as at the
            # beginning of acquisition. The manager sees no PID and tries sync.
            markers = log_path.read_bytes().count(b"agentOwnerRun") if log_path.exists() else 0
            with lock_path.open("a+") as holder:
                fcntl.flock(holder, fcntl.LOCK_EX)
                holder.truncate(0)
                before = os.fstat(holder.fileno())
                old_log_size = log_path.stat().st_size if log_path.exists() else 0
                status = ensure()
                appended = log_path.read_bytes()[old_log_size:]
                assert b"store is locked" in appended, "packaged sync did not hit kernel contention"
                record = record_path.read_bytes() if record_path.exists() else None
                assert record == previous_record, (
                    f"contender published owner PID {status.get('ownerPid')} while another process held the kernel lock"
                )
                assert log_path.read_bytes().count(b"agentOwnerRun") == markers
                after = lock_path.stat()
                assert (before.st_dev, before.st_ino) == (after.st_dev, after.st_ino)

        contend(None)
        print("PASS packaged agent: contended sync published neither owner nor run marker")

        # Once the holder exits, the packaged sync can take the original lock.
        # The empty store reaches the auth guard without connecting to WhatsApp.
        status = ensure()
        record = json.loads(record_path.read_text())
        with log_path.open("rb") as log:
            log.seek(record["logStartOffset"])
            assert json.loads(log.readline()) == {"agentOwnerRun": record["runId"]}
            assert "not authenticated" in log.read().decode()
        assert record["pid"] == status["lockPid"] == status["ownerPid"]
        assert (record["logDev"], record["logIno"]) == (
            log_path.stat().st_dev, log_path.stat().st_ino,
        )
        assert not status["sendReady"]
        print("PASS packaged agent: lock winner published matching owner and marker and reached auth guard")
        contend(record_path.read_bytes())
        print("PASS packaged agent: rejected startup preserved the previous owner record and run marker")


if __name__ == "__main__":
    main()
