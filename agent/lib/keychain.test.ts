import test from "node:test";
import assert from "node:assert/strict";
import { macOSKeychain, validateReference, validateApiKey, type SecurityRunner } from "./keychain.ts";

const reference = { service: "wacli.fixture", account: "fixture" };
const key = "sk-fixture_ONLY";

test("Keychain creation sends the credential only through native stdin and verifies exact readback", async () => {
  const calls: { args: string[]; input?: string }[] = [];
  const runner: SecurityRunner = async (args, input) => {
    calls.push({ args, input });
    return { code: 0, stdout: args.includes("-w") ? `${key}\n` : "ignored private output" };
  };
  await macOSKeychain(runner, "darwin").create(reference, key);
  assert.deepEqual(calls, [
    { args: ["-i"], input: `add-generic-password -s wacli.fixture -a fixture -w ${key}\n` },
    { args: ["find-generic-password", "-s", "wacli.fixture", "-a", "fixture", "-w"], input: undefined },
  ]);
  assert.ok(calls.every((call) => !call.args.join(" ").includes(key)));
  assert.ok(!calls[0].input!.includes("-U") && !calls[0].input!.includes("-A"));
});

test("only item-not-found is absence; denied/locked/process failures stay sanitized", async () => {
  for (const code of [44, 36, null]) {
    const keychain = macOSKeychain(async () => ({ code, stdout: key }), "darwin");
    if (code === 44) {
      assert.equal(await keychain.exists(reference), false);
      await assert.rejects(keychain.read(reference), /not found/);
    } else {
      await assert.rejects(keychain.exists(reference), (error: Error) => !error.message.includes(key) && /lookup failed/.test(error.message));
      await assert.rejects(keychain.read(reference), (error: Error) => !error.message.includes(key) && /access failed/.test(error.message));
    }
  }
  const exploding = macOSKeychain(async () => { throw new Error(`native echoed ${key}`); }, "darwin");
  await assert.rejects(exploding.read(reference), (error: Error) => !error.message.includes(key) && !error.cause);
});

test("interactive process success does not prove creation, and empty/invalid credentials are rejected", async () => {
  const keychain = macOSKeychain(async () => ({ code: 0, stdout: "wrong-item" }), "darwin");
  await assert.rejects(keychain.create(reference, key), /verification failed/);
  for (const stdout of ["", "key with whitespace", "key\nextra", "key;command"]) {
    await assert.rejects(macOSKeychain(async () => ({ code: 0, stdout }), "darwin").read(reference), /single supported/);
  }
});

test("invalid reference/key input and non-macOS platform fail before native commands", async () => {
  let called = false;
  const keychain = macOSKeychain(async () => { called = true; return { code: 0, stdout: key }; }, "darwin");
  for (const value of [null, {}, { service: "x\ncommand", account: "a" }, { service: "-s", account: "a" }, { service: "x", account: "a'\"" }]) {
    assert.throws(() => validateReference(value), /Invalid Keychain/);
    await assert.rejects(keychain.read(value as any), /Invalid Keychain/);
  }
  for (const value of ["", "key\ncommand", "key;command", "key$expand", "a".repeat(4097)]) {
    assert.throws(() => validateApiKey(value), /single supported/);
    await assert.rejects(keychain.create(reference, value), /single supported/);
  }
  await assert.rejects(macOSKeychain(async () => { called = true; return { code: 0, stdout: key }; }, "linux").read(reference), /unavailable/);
  assert.equal(called, false);
});
