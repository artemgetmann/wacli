// macOS OS-level acceptance: disposable processes, kernel flock and IPC only.
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import {spawnSync} from "node:child_process";
import {fileURLToPath} from "node:url";

assert.equal(process.platform, "darwin", "the permission-denial acceptance uses macOS sandbox-exec");
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const dir = await fs.mkdtemp("/tmp/wo-");
const binary = path.join(dir, "wacli");
const packaged = process.argv.includes("--packaged");
const cli = path.join(root, "dist/wacli");
const helper = path.join(root, "agent/lib/owner/live.ts");
const store = path.join(dir, "store");
await fs.mkdir(store, {mode:0o700});
const env = {...process.env, WACLI_AGENT_BINARY:binary};
let owner;
function run(action, profile, source=helper, packagedEntry=packaged) {
  const executable = packagedEntry ? cli : process.execPath;
  const entry = packagedEntry ? ["--store", store, "agent", "live", action] : ["--experimental-strip-types", source, action, "--store", store];
  const args = [...entry, "--json", "--settle-ms","1500","--grace-ms","1500"];
  const result = profile
    ? spawnSync("/usr/bin/sandbox-exec",["-p",profile,executable,...args],{env,encoding:"utf8",timeout:10000})
    : spawnSync(executable,args,{env,encoding:"utf8",timeout:10000});
  assert.ifError(result.error);
  assert.ok(result.stdout.trim(), `helper returned no JSON: ${result.stderr}`);
  return {exit:result.status, data:JSON.parse(result.stdout)};
}
async function evidence() {
  const result={};
  for(const name of ["LOCK","openclaw-sync-owner.json","openclaw-sync.log"]) result[name]=await fs.readFile(path.join(store,name),"utf8");
  result.lockInode=(await fs.stat(path.join(store,"LOCK"))).ino;
  return result;
}
try {
  const build=spawnSync("go",["build","-o",binary,"./scripts/testdata/owner-fixture"],{cwd:root,encoding:"utf8"});
  assert.equal(build.status,0,build.stderr);
  // Seed a real offline flock/IPC holder. In packaged mode every subsequent
  // status, ensure and stop below uses the actual release entry point.
  const started=run("ensure",undefined,helper,false);owner=started.data.ownerPid;
  assert.equal(started.exit,0);assert.equal(started.data.sendReady,true);
  const initial=await evidence();
  const denyProbe="(version 1)(allow default)(deny signal)(deny process-info*)";
  if (process.env.WACLI_OWNER_BASELINE_HELPER) {
    const baseline=run("status",denyProbe,process.env.WACLI_OWNER_BASELINE_HELPER,false);
    assert.equal(baseline.data.ownerRunning,false);
    assert.equal(baseline.data.lockPidRunning,false);
    assert.deepEqual(await evidence(),initial);
    console.log("PASS safe baseline contrast: status misclassifies an OS-denied live process as absent; no baseline mutation attempted");
  }
  const denied=run("ensure",denyProbe);
  assert.notEqual(denied.exit,0);assert.equal(denied.data.ownerIdentityUnknown,true);
  assert.deepEqual(await evidence(),initial);
  const noCommand=run("stop","(version 1)(allow default)(deny process-info*)");
  assert.notEqual(noCommand.exit,0);assert.equal(noCommand.data.stopReason,"identity_unknown");
  assert.deepEqual(await evidence(),initial);
  const noPs=spawnSync(process.execPath,["--experimental-strip-types",helper,"stop","--store",store,"--json"],{env:{...env,PATH:dir},encoding:"utf8",timeout:10000});
  assert.equal(JSON.parse(noPs.stdout).stopReason,"identity_unknown");
  assert.deepEqual(await evidence(),initial);
  // A second real contender cannot take the actual flock, even after refusals.
  const contender=spawnSync(binary,["--store",store,"sync","--follow","--events"],{encoding:"utf8",timeout:3000});
  assert.equal(contender.status,3);
  assert.deepEqual(await evidence(),initial);
  assert.equal(run("status").data.sendReady,true);
  const stopped=run("stop");assert.equal(stopped.exit,0);
  assert.equal((await fs.stat(path.join(store,"LOCK"))).ino,initial.lockInode);
  owner=undefined;
  if (packaged) {
    // With no credentials the real packaged sync can start, claim the lock
    // and exit at its authentication guard. This is deliberately offline.
    const restarted = run("ensure");
    const recorded = JSON.parse(await fs.readFile(path.join(store, "openclaw-sync-owner.json"), "utf8"));
    assert.notEqual(recorded.pid, started.data.ownerPid);
    assert.deepEqual(recorded.command, [cli, "--store", store, "sync", "--follow", "--events"]);
    assert.match(await fs.readFile(path.join(store, "LOCK"), "utf8"), new RegExp("pid=" + recorded.pid + "(?:\\s|$)"));
    assert.equal((await fs.stat(path.join(store, "LOCK"))).ino, initial.lockInode);
    assert.match(await fs.readFile(path.join(store, "openclaw-sync.log"), "utf8"), /not authenticated/i);
    assert.equal(restarted.data.sendReady, false);
    console.log("PASS packaged agent live: blocked denied ensure/stop, kernel excludes contender, healthy fixture readiness, normal stop, actual sync restart reached authentication guard with unchanged lock inode");
  } else {
    const restarted=run("ensure");owner=restarted.data.ownerPid;
    assert.equal(restarted.data.sendReady,true);
    assert.equal((await fs.stat(path.join(store,"LOCK"))).ino,initial.lockInode);
    console.log("PASS actual OS denial, missing process inspector, unchanged owner/log/lock, kernel exclusion, readiness, stop/start with stable lock inode");
  }
} finally {
  if(owner) { try { run("stop"); } catch { try {process.kill(owner,"SIGTERM");} catch {} } }
  await fs.rm(dir,{recursive:true,force:true});
}
