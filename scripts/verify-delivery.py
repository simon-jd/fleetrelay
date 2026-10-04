#!/usr/bin/env python3
"""Collect local evidence with running tests/integration/compose.yml services.

Requires Python 3.12+, Go, Buf, Docker CLI and installed web dependencies.
Use only the disposable test DSN and Redis address documented in the runbook.
"""
import hashlib
import io
import json
import os
import pathlib
import platform
import shutil
import subprocess
import tarfile
import tempfile
import time
from datetime import datetime, timezone

ROOT = pathlib.Path(__file__).resolve().parents[1]
OUT = ROOT / "docs/evidence/2026-09-06"
OUT.mkdir(parents=True, exist_ok=True)
ENV = dict(os.environ, GOCACHE=os.environ.get("GOCACHE", "/tmp/fleetrelay-go-cache"),
           FLEETRELAY_REQUIRE_INTEGRATION="1")
for key in ("FLEETRELAY_TEST_DATABASE_URL", "FLEETRELAY_TEST_REDIS_ADDR"):
    if not ENV.get(key):
        raise SystemExit(f"{key} must explicitly select the disposable test services")
records = []


def run(name, argv, cwd=ROOT, expected=0):
    start = time.monotonic()
    result = subprocess.run(argv, cwd=cwd, env=ENV, stdout=subprocess.PIPE,
                            stderr=subprocess.STDOUT, text=True, timeout=180)
    (OUT / (name + ".log")).write_text(result.stdout)
    record = {"name": name, "command": argv, "cwd": str(cwd), "exit_code": result.returncode,
              "expected_exit_code": expected, "elapsed_seconds": round(time.monotonic()-start, 3),
              "raw_output": name + ".log", "passed": result.returncode == expected}
    records.append(record)
    print(f"{name}: exit={result.returncode} expected={expected}", flush=True)
    if not record["passed"]:
        print(result.stdout)
        raise RuntimeError(name + " failed")
    return result.stdout


with tempfile.TemporaryDirectory(prefix="fleetrelay-verify-bin-") as binaries:
    ENV["FLEETRELAY_TEST_BIN_DIR"] = binaries
    run("race-build", ["go", "build", "-race", "-o", binaries + "/", "./cmd/..."])
    raw = run("go-race", ["go", "test", "-race", "-count=1", "-json", "./..."])
    events = [json.loads(line) for line in raw.splitlines() if line.startswith("{")]
    outcomes = [e for e in events if e.get("Test") and "/" not in e["Test"]
                and e["Action"] in ("pass", "fail", "skip")]
    counts = {action: sum(e["Action"] == action for e in outcomes) for action in ("pass", "fail", "skip")}
    if counts["skip"] or counts["fail"]:
        raise RuntimeError(f"strict verification requires no failed or skipped tests: {counts}")
    run("go-vet", ["go", "vet", "./..."])
    run("go-build", ["go", "build", "./cmd/..."])
    formatted = run("gofmt", ["gofmt", "-l", "cmd", "internal", "tests", "gen"])
    if formatted.strip():
        raise RuntimeError("gofmt found unformatted files")
    for name, args in (("buf-format", ["format", "--diff", "--exit-code"]),
                       ("buf-lint", ["lint"]), ("buf-build", ["build"])):
        run(name, ["buf", *args])
    run("compose", ["docker", "compose", "--env-file", "deploy/compose/.env.example", "-f",
                    "deploy/compose/docker-compose.yml", "config", "--quiet"])
    run("integration-compose", ["docker", "compose", "-f", "tests/integration/compose.yml", "config", "--quiet"])
    for task in ("lint", "test", "build"):
        run("web-" + task, ["npm", "run", task], ROOT / "web")

# Negative control: the same atomicity oracle must fail on the old code.
with tempfile.TemporaryDirectory(prefix="fleetrelay-baseline-") as baseline:
    archive = subprocess.check_output(["git", "archive", "84206e1"], cwd=ROOT)
    with tarfile.open(fileobj=io.BytesIO(archive)) as tf:
        tf.extractall(baseline, filter="data")
    baseline = pathlib.Path(baseline)
    shutil.copytree(ROOT / "internal/testutil", baseline / "internal/testutil")
    shutil.copyfile(OUT / "baseline-regression.go.txt", baseline / "internal/control/postgres/atomicity_regression_test.go")
    result = run("baseline-regression", ["go", "test", "-race", "-count=1", "-run",
                 "TestAttemptAndDeliveryCommitTogether", "-v", "./internal/control/postgres"], baseline, expected=1)
    if "attempt committed without its delivery intent" not in result:
        raise RuntimeError("baseline failed for an unexpected reason")

environment = {"os": platform.platform(), "machine": platform.machine(), "logical_cpus": os.cpu_count(),
               "cpu": next((line.split(":",1)[1].strip() for line in pathlib.Path("/proc/cpuinfo").read_text().splitlines()
                             if line.startswith("model name")), "unknown"),
               "memory": pathlib.Path("/proc/meminfo").read_text().splitlines()[0],
               "go": subprocess.check_output(["go", "version"], text=True).strip(),
               "node": subprocess.check_output(["node", "--version"], text=True).strip(),
               "docker": subprocess.check_output(["docker", "version", "--format", "{{.Server.Version}}"],text=True).strip(),
               "postgres": subprocess.check_output(["docker", "compose", "-f", "tests/integration/compose.yml", "exec", "-T", "postgres", "psql", "-U", "fleetrelay", "-d", "fleetrelay_test", "-Atc", "SHOW server_version"],text=True).strip(),
               "redis_image": "redis:8.2-alpine", "topology": "local Linux; PostgreSQL/Redis Docker containers; four race-built Go subprocesses; deterministic device simulator"}
summary = {"verified_at": datetime.now(timezone.utc).isoformat(),
           "base_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT,text=True).strip(),
           "revision_status": "uncommitted_working_tree", "environment": environment,
           "top_level_go_tests": counts, "checks": records,
           "performance_claim": None,
           "limitations": ["Single local machine; no cloud, production or physical hardware",
                           "Fault hooks inject XADD boundary failures; connection reopen is not a process SIGKILL campaign",
                           "No throughput/latency benchmark or multi-node HA certification"]}
paths = subprocess.check_output(["git", "ls-files", "--cached", "--others", "--exclude-standard"], cwd=ROOT,text=True).splitlines()
summary["source_sha256"] = {p: hashlib.sha256((ROOT/p).read_bytes()).hexdigest() for p in sorted(set(paths))
                            if (ROOT/p).is_file() and not p.startswith("docs/evidence/")}
(OUT / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
print(json.dumps({"top_level_go_tests": counts, "checks":len(records), "report":str(OUT / 'summary.json')}))
