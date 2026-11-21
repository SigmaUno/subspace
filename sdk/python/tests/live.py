from __future__ import annotations

import os
import secrets
import shutil
import socket
import subprocess
import tempfile
import time
import unittest
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]


@dataclass
class LiveServer:
    url: str
    owner_token: str
    _proc: subprocess.Popen
    _workdir: str
    _log: object

    def stop(self) -> None:
        self._proc.terminate()
        try:
            self._proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self._proc.kill()
            self._proc.wait()
        self._log.close()
        shutil.rmtree(self._workdir, ignore_errors=True)


def start() -> LiveServer:
    go = shutil.which("go")
    if go is None:
        raise unittest.SkipTest("go toolchain not available")
    workdir = tempfile.mkdtemp(prefix="subspace-sdk-")
    binary = os.path.join(workdir, "subspace")
    build = subprocess.run(
        [go, "build", "-o", binary, "./cmd/subspace"],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
        timeout=900,
    )
    if build.returncode != 0:
        shutil.rmtree(workdir, ignore_errors=True)
        raise RuntimeError(f"go build failed:\n{build.stderr[-4000:]}")

    port = _free_port()
    url = f"http://127.0.0.1:{port}"
    owner_token = secrets.token_urlsafe(32)
    env = {k: v for k, v in os.environ.items() if not k.startswith("SUBSPACE_")}
    env.update(
        {
            "SUBSPACE_ADDR": f"127.0.0.1:{port}",
            "SUBSPACE_DB": os.path.join(workdir, "subspace.db"),
            "SUBSPACE_OWNER_TOKEN": owner_token,
            "SUBSPACE_PUBLIC_URL": url,
            "SUBSPACE_BACKUP_INTERVAL": "0",
            "SUBSPACE_LOG_LEVEL": "warn",
        }
    )
    log = open(os.path.join(workdir, "server.log"), "w")
    proc = subprocess.Popen([binary], env=env, cwd=workdir, stdout=log, stderr=subprocess.STDOUT)
    server = LiveServer(url, owner_token, proc, workdir, log)
    try:
        _wait_healthy(server)
    except Exception:
        server.stop()
        raise
    return server


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _wait_healthy(server: LiveServer, timeout: float = 60.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if server._proc.poll() is not None:
            raise RuntimeError(f"subspace exited early with code {server._proc.returncode}")
        try:
            with urllib.request.urlopen(server.url + "/healthz", timeout=1) as resp:
                if resp.status == 200:
                    return
        except (urllib.error.URLError, OSError):
            time.sleep(0.1)
    raise RuntimeError("subspace did not become healthy in time")
