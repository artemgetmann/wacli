import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { defaultStoreDir } from "./store.ts";
import { commandLooksLikeExpectedOwner, parseArgs as parseLive } from "./live.ts";
import { parseArgs as parseHealth } from "./health.ts";
import { parseArgs as parseReply } from "../read/recent-reply.ts";
import { defaultDbPath, defaultStateDir, parseArgs as parseMonitor } from "../read/monitor-check.ts";

test("all helper defaults honor the same selected store", () => {
  const agent = process.env.WACLI_AGENT_STORE;
  const normal = process.env.WACLI_STORE_DIR;
  try {
    process.env.WACLI_STORE_DIR = path.join(os.tmpdir(), "wacli-configured");
    delete process.env.WACLI_AGENT_STORE;
    assert.equal(defaultStoreDir(), process.env.WACLI_STORE_DIR);
    process.env.WACLI_AGENT_STORE = path.join(os.tmpdir(), "wacli-agent-selected");
    const selected = process.env.WACLI_AGENT_STORE;
    assert.equal(defaultStoreDir(), selected);
    assert.equal(parseLive(["status"]).storeDir, selected);
    assert.equal(parseHealth([]).store, selected);
    assert.equal(commandLooksLikeExpectedOwner("wacli sync --follow --json", selected), false);
    assert.equal(commandLooksLikeExpectedOwner("wacli sync --follow --json", process.env.WACLI_STORE_DIR), true);
    assert.equal(parseReply(["--target", "111"]).dbPath, path.join(selected, "wacli.db"));
    assert.equal(parseMonitor(["--target", "111"]).dbPath, path.join(selected, "wacli.db"));
    assert.equal(defaultDbPath(), path.join(selected, "wacli.db"));
    assert.equal(defaultStateDir(), path.join(selected, "agent-monitor-state"));
    assert.equal(parseLive(["status", "--store", "/tmp/explicit"]).storeDir, "/tmp/explicit");
    assert.equal(parseHealth(["--store", "/tmp/explicit"]).store, "/tmp/explicit");
  } finally {
    if (agent === undefined) delete process.env.WACLI_AGENT_STORE;
    else process.env.WACLI_AGENT_STORE = agent;
    if (normal === undefined) delete process.env.WACLI_STORE_DIR;
    else process.env.WACLI_STORE_DIR = normal;
  }
});

test("read-only CLI permits status and rejects owner and refresh mutations", () => {
  const dir = path.dirname(fileURLToPath(import.meta.url));
  const env = { ...process.env, WACLI_READONLY: "true" };
  const run = (script: string, args: string[]) => spawnSync(
    process.execPath,
    ["--experimental-strip-types", path.join(dir, script), ...args],
    { encoding: "utf8", env },
  );
  const store = path.join(os.tmpdir(), "wacli-readonly-guard");
  assert.equal(run("live.ts", ["status", "--store", store, "--json"]).status, 0);
  for (const action of ["ensure", "stop"]) {
    const result = run("live.ts", [action, "--store", store, "--json"]);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /read-only mode/);
  }
  const health = run("health.ts", ["--refresh", "--store", store, "--json"]);
  assert.equal(health.status, 1);
  assert.match(health.stderr, /read-only mode/);
});
