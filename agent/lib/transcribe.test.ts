import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { transcribe, configureTranscription } from "./transcribe.ts";

const profile = { provider: "openai" as const, model: "gpt-4o-mini-transcribe", apiKey: "test-only" };
test("read-only mode refuses credential provisioning before stdin or files", async () => {
  const previous = process.env.WACLI_READONLY;
  process.env.WACLI_READONLY = "on";
  try { await assert.rejects(configureTranscription([]), /Read-only mode/); }
  finally { if (previous === undefined) delete process.env.WACLI_READONLY; else process.env.WACLI_READONLY = previous; }
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
