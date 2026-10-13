import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { transcribe, configureTranscription, loadProfile } from "./transcribe.ts";
import type { Keychain } from "./keychain.ts";

const profile = { provider: "openai" as const, model: "gpt-4o-mini-transcribe", apiKey: "test-only" };
test("read-only mode refuses credential provisioning before stdin or files", async () => {
  const previous = process.env.WACLI_READONLY;
  process.env.WACLI_READONLY = "on";
  try {
    for (const flags of [[], ["--migrate-profile"]]) {
      await assert.rejects(configureTranscription(flags, { input: { async *[Symbol.asyncIterator]() { throw new Error("must not consume input"); } } }), /Read-only mode/);
    }
  }
  finally { if (previous === undefined) delete process.env.WACLI_READONLY; else process.env.WACLI_READONLY = previous; }
});

const reference = { service: "wacli.fixture", account: "fixture" };
const fixtureKey = "sk-fixture-only";
const configured = { provider: "openai", model: "pinned-model", apiKey: fixtureKey };
async function* credentialInput(value = fixtureKey) { yield value; }
function fixtureKeychain(initial?: string) {
  let value = initial;
  const events: string[] = [];
  const keychain: Keychain = {
    async exists() { events.push("exists"); return value !== undefined; },
    async read() { events.push("read"); if (value === undefined) throw new Error("fixture missing"); return value; },
    async create(_reference, key) { events.push("create"); if (value !== undefined) throw new Error("fixture duplicate"); value = key; },
  };
  return { keychain, events };
}
async function withStore(run: (dir: string, target: string) => Promise<void>) {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "wacli-profile-test-"));
  try { await run(dir, path.join(dir, "agent-transcription.json")); }
  finally { await fs.rm(dir, { recursive: true, force: true }); }
}

test("macOS provisioning stores only a reference, and legacy/non-macOS/env credentials still work", async () => {
  const previous = process.env.OPENAI_API_KEY;
  delete process.env.OPENAI_API_KEY;
  try {
    await withStore(async (dir, target) => {
      const { keychain, events } = fixtureKeychain();
      const result = await configureTranscription(["--store", dir, "--model", "pinned-model", "--keychain-service", reference.service, "--keychain-account", reference.account], { platform: "darwin", keychain, input: credentialInput() });
      const saved = JSON.parse(await fs.readFile(target, "utf8"));
      assert.deepEqual(saved, { provider: "openai", model: "pinned-model", keychain: reference });
      assert.equal((await fs.stat(target)).mode & 0o777, 0o600);
      assert.deepEqual(events, ["exists", "create", "read"]);
      assert.ok(!JSON.stringify(result).includes(fixtureKey));
      assert.deepEqual(await loadProfile(dir, undefined, keychain), configured);
    });
    await withStore(async (dir, target) => {
      await configureTranscription(["--store", dir, "--model", "pinned-model"], { platform: "linux", input: credentialInput() });
      assert.deepEqual(JSON.parse(await fs.readFile(target, "utf8")), configured);
      assert.deepEqual(await loadProfile(dir), configured);
    });
    await withStore(async (dir) => {
      await assert.rejects(loadProfile(dir), /not configured/);
      process.env.OPENAI_API_KEY = fixtureKey;
      assert.equal((await loadProfile(dir)).apiKey, fixtureKey);
      delete process.env.OPENAI_API_KEY;
    });
  } finally { if (previous === undefined) delete process.env.OPENAI_API_KEY; else process.env.OPENAI_API_KEY = previous; }
});

test("explicit migration preserves model, verifies Keychain, then atomically removes plaintext", async () => {
  for (const existingItem of [undefined, fixtureKey]) {
    await withStore(async (dir, target) => {
      await fs.writeFile(target, JSON.stringify(configured), { mode: 0o600 });
      const { keychain, events } = fixtureKeychain(existingItem);
      const result = await configureTranscription(["--store", dir, "--migrate-profile"], { platform: "darwin", keychain, input: { async *[Symbol.asyncIterator]() { throw new Error("migration must not read stdin"); } } });
      assert.equal(result.model, "pinned-model");
      assert.equal(result.migrated, true);
      assert.ok(!JSON.stringify(result).includes(fixtureKey));
      const saved = JSON.parse(await fs.readFile(target, "utf8"));
      assert.deepEqual(saved, { provider: "openai", model: "pinned-model", keychain: result.keychain });
      assert.equal(saved.keychain.service, "wacli.transcription.openai");
      assert.match(saved.keychain.account, /^store-[a-f0-9]{24}$/);
      assert.equal((await fs.stat(target)).mode & 0o777, 0o600);
      assert.deepEqual(events, existingItem ? ["exists", "read", "read"] : ["exists", "create", "read"]);
      assert.deepEqual(await fs.readdir(dir), ["agent-transcription.json"]);
    });
  }
});

test("default Keychain accounts are stable per store and distinct across stores without leaking paths", async () => {
  await withStore(async (first) => {
    await withStore(async (second) => {
      const accounts: string[] = [];
      for (const dir of [first, second]) {
        const result = await configureTranscription(["--store", dir], { platform: "darwin", keychain: fixtureKeychain().keychain, input: credentialInput() });
        assert.ok(!JSON.stringify(result).includes(dir));
        accounts.push(result.keychain!.account);
      }
      assert.notEqual(accounts[0], accounts[1]);
      await fs.unlink(path.join(first, "agent-transcription.json"));
      const repeated = await configureTranscription(["--store", first, "--credential-store", "keychain"], { platform: "darwin", keychain: fixtureKeychain().keychain, input: credentialInput() });
      assert.equal(repeated.keychain!.account, accounts[0]);
    });
  });
});

test("failed migration keeps exact plaintext source and does not overwrite a different Keychain item", async () => {
  for (const failure of ["different-item", "creation-failed", "readback-failed", "source-changed"]) {
    await withStore(async (dir, target) => {
      const raw = JSON.stringify(configured);
      await fs.writeFile(target, raw, { mode: 0o600 });
      const fixture = fixtureKeychain(failure === "different-item" ? "sk-other" : undefined);
      let expected = raw;
      if (failure === "creation-failed") fixture.keychain.create = async () => { throw new Error("fixture creation failed"); };
      if (failure === "readback-failed") fixture.keychain.read = async () => "sk-unexpected";
      if (failure === "source-changed") {
        const originalRead = fixture.keychain.read;
        fixture.keychain.read = async (ref) => { expected = JSON.stringify({ ...configured, model: "changed-model" }); await fs.writeFile(target, expected); return originalRead(ref); };
      }
      await assert.rejects(configureTranscription(["--store", dir, "--migrate-profile"], { platform: "darwin", keychain: fixture.keychain }));
      assert.equal(await fs.readFile(target, "utf8"), expected);
      assert.ok(!fixture.events.includes("create") || failure !== "different-item");
      assert.deepEqual(await fs.readdir(dir), ["agent-transcription.json"]);
    });
  }
});

test("profiles reject unsafe permissions, symlinks, invalid references, mixed storage and malformed secret JSON", async () => {
  await withStore(async (dir, target) => {
    for (const body of [
      JSON.stringify({ provider: "openai", model: "pinned-model", keychain: { service: "bad\ncommand", account: "fixture" } }),
      JSON.stringify({ ...configured, keychain: reference }),
      `{"apiKey":"${fixtureKey}",INVALID`,
      JSON.stringify({ ...configured, provider: "other-provider" }),
    ]) {
      await fs.writeFile(target, body, { mode: 0o600 });
      await assert.rejects(loadProfile(dir), (error: Error) => !error.message.includes(fixtureKey));
    }
    await fs.writeFile(target, JSON.stringify(configured));
    await fs.chmod(target, 0o644);
    await assert.rejects(loadProfile(dir), /owner-only/);
    await assert.rejects(configureTranscription(["--store", dir, "--migrate-profile"], { platform: "darwin", keychain: fixtureKeychain().keychain }), /owner-only/);
    await fs.unlink(target);
    const source = path.join(dir, "original.json");
    await fs.writeFile(source, JSON.stringify(configured), { mode: 0o600 });
    await fs.symlink(source, target);
    await assert.rejects(loadProfile(dir), /opened safely/);
  });
});

test("configuration rejects invalid options, missing source, duplicates and existing items before input", async () => {
  await withStore(async (dir, target) => {
    const input = { async *[Symbol.asyncIterator]() { throw new Error("must not consume input"); } };
    for (const flags of [["--credential-store", "unknown"], ["--credential-store", "profile", "--migrate-profile"], ["--keychain-account", "a b"], ["--model", "a b"], ["--migrate-profile"]]) {
      await assert.rejects(configureTranscription(["--store", dir, ...flags], { platform: "darwin", input, keychain: fixtureKeychain().keychain }), (error: Error) => !/consume input/.test(error.message));
    }
    await assert.rejects(configureTranscription(["--store", dir, "--credential-store", "keychain"], { platform: "linux", input }), /unavailable/);
    await assert.rejects(configureTranscription(["--store", dir], { platform: "darwin", input, keychain: fixtureKeychain(fixtureKey).keychain }), /already exists/);
    await fs.writeFile(target, JSON.stringify(configured), { mode: 0o600 });
    await assert.rejects(configureTranscription(["--store", dir], { platform: "darwin", input, keychain: fixtureKeychain().keychain }), /already exists/);
    assert.equal(await fs.readFile(target, "utf8"), JSON.stringify(configured));
  });
});

test("Keychain read failure is not silently replaced by another stored provider credential", async () => {
  const previous = process.env.OPENAI_API_KEY;
  delete process.env.OPENAI_API_KEY;
  try {
    await withStore(async (dir, target) => {
      await fs.writeFile(target, JSON.stringify({ provider: "openai", model: "pinned-model", keychain: reference }), { mode: 0o600 });
      await assert.rejects(loadProfile(dir, undefined, fixtureKeychain().keychain), /fixture missing/);
      process.env.OPENAI_API_KEY = fixtureKey;
      assert.equal((await loadProfile(dir, undefined, fixtureKeychain().keychain)).apiKey, fixtureKey);
    });
  } finally { if (previous === undefined) delete process.env.OPENAI_API_KEY; else process.env.OPENAI_API_KEY = previous; }
});
test("named and extensionless Opus normalize before provider; original remains", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "wacli-audio-test-"));
  try {
    const source = path.join(dir, "source.oga");
    execFileSync("ffmpeg", ["-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.3", "-c:a", "libopus", source]);
    for (const name of [source, path.join(dir, "extensionless")]) {
      if (name !== source) await fs.copyFile(source, name);
      let uploaded: string | undefined;
      const result = await transcribe(["--file", name], async (file) => { uploaded = file; assert.equal(path.extname(file), ".mp3"); return "fixture transcript"; }, profile);
      assert.equal(result.normalized, true);
      assert.equal(result.inputCodec, "opus");
      assert.equal(result.text, "fixture transcript");
      await assert.rejects(fs.stat(uploaded!), /ENOENT/);
      assert.ok((await fs.stat(name)).size > 0);
    }
  } finally { await fs.rm(dir, { recursive: true, force: true }); }
});

test("MP3 fast path and provider error/silence remain distinct", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "wacli-audio-test-"));
  try {
    const source = path.join(dir, "source.mp3");
    execFileSync("ffmpeg", ["-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.3", source]);
    const canonicalSource = await fs.realpath(source);
    const result = await transcribe(["--file", source], async (file) => { assert.equal(file, canonicalSource); return ""; }, profile);
    assert.equal(result.normalized, false);
    assert.equal(result.status, "silence");
    await assert.rejects(transcribe(["--file", source], async () => { throw new Error("provider unavailable"); }, profile), /provider unavailable/);
  } finally { await fs.rm(dir, { recursive: true, force: true }); }
});

test("provider transport/JSON errors suppress secrets, and successful output excludes credentials", async () => {
  const originalFetch = globalThis.fetch;
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "wacli-provider-test-"));
  try {
    const source = path.join(dir, "source.mp3");
    execFileSync("ffmpeg", ["-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.3", source]);
    for (const failure of ["transport", "json", "http"]) {
      globalThis.fetch = (async (_url, request) => {
        assert.equal((request!.headers as Record<string, string>).Authorization, `Bearer ${profile.apiKey}`);
        assert.equal((request!.body as FormData).get("model"), profile.model);
        if (failure === "transport") throw new Error(`request headers contain ${profile.apiKey}`);
        return { ok: failure !== "http", status: 401, async json() { throw new Error(`invalid response ${profile.apiKey}`); } } as Response;
      }) as typeof fetch;
      await assert.rejects(transcribe(["--file", source], undefined, profile), (error: Error) => !error.message.includes(profile.apiKey) && !error.cause);
    }
    globalThis.fetch = (async () => ({ ok: true, async json() { return { text: "fixture transcript" }; } } as Response)) as typeof fetch;
    const result = await transcribe(["--file", source], undefined, profile);
    assert.equal(result.model, profile.model);
    assert.equal(result.text, "fixture transcript");
    assert.ok(!JSON.stringify(result).includes(profile.apiKey));
  } finally { globalThis.fetch = originalFetch; await fs.rm(dir, { recursive: true, force: true }); }
});
