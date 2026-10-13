import { execFile } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { defaultStore, parseFlags } from "./runtime.ts";

type Profile = { provider: "openai"; model: string; apiKey: string };
type Upload = (file: string, profile: Profile, language?: string) => Promise<string>;
const maxUploadBytes = 25 * 1024 * 1024;

function mediaCommand(binary: string, args: string[], timeout = 60_000): Promise<string> {
  return new Promise((resolve, reject) => execFile(binary, args, { timeout, maxBuffer: 2 * 1024 * 1024 }, (error, stdout, stderr) => {
    if (error) reject(new Error(`${binary} failed (${error.code || "timeout"}): ${String(stderr).slice(-800)}`));
    else resolve(String(stdout));
  }));
}

// Credentials are explicit standalone configuration, not runtime imports from
// another project. Provisioning reads the key from stdin and never returns it.
export async function configureTranscription(argv: string[]) {
  const flags = parseFlags(argv, ["json"]);
  for (const key of Object.keys(flags)) if (!["store", "model", "json"].includes(key)) throw new Error(`Unsupported --${key}`);
  const chunks: Buffer[] = [];
  let length = 0;
  for await (const chunk of process.stdin) {
    length += chunk.length;
    if (length > 4096) throw new Error("Credential input is too large");
    chunks.push(Buffer.from(chunk));
  }
  const apiKey = Buffer.concat(chunks).toString("utf8").trim();
  if (!apiKey || /\s/.test(apiKey)) throw new Error("A single API key is required on stdin");
  const model = String(flags.model || "gpt-4o-mini-transcribe");
  if (!/^[a-zA-Z0-9._-]+$/.test(model)) throw new Error("Invalid transcription model identifier");
  const store = path.resolve(String(flags.store || defaultStore()));
  await fs.mkdir(store, { recursive: true, mode: 0o700 });
  const target = path.join(store, "agent-transcription.json");
  // Exclusive creation avoids silently replacing a separately configured
  // provider/account during deployment. Updating it is a deliberate operation.
  await fs.writeFile(target, JSON.stringify({ provider: "openai", model, apiKey }), { mode: 0o600, flag: "wx" });
  return { ok: true, provider: "openai", model, configured: true };
}

async function loadProfile(store: string, modelOverride?: string): Promise<Profile> {
  let profile: any = {};
  try {
    const target = path.join(store, "agent-transcription.json");
    const stat = await fs.stat(target);
    if ((stat.mode & 0o077) !== 0) throw new Error("Transcription credentials must be owner-only (0600)");
    profile = JSON.parse(await fs.readFile(target, "utf8"));
  } catch (error: any) { if (error.code !== "ENOENT") throw error; }
  const apiKey = process.env.OPENAI_API_KEY || profile.apiKey;
  if (!apiKey) throw new Error("Standalone OpenAI credentials are not configured; use agent configure-transcription with the key on stdin");
  if (profile.provider && profile.provider !== "openai") throw new Error("Unsupported configured provider; no automatic provider switch");
  return { provider: "openai", apiKey, model: modelOverride || profile.model || "gpt-4o-mini-transcribe" };
}

// The first adapter keeps the user's configured OpenAI model. There is no
// automatic fallback to a local model or another paid provider on failure.
const uploadOpenAI: Upload = async (file, profile, language) => {
  const data = await fs.readFile(file);
  if (data.length > maxUploadBytes) throw new Error("Normalized audio exceeds provider upload limit");
  const form = new FormData();
  form.set("file", new Blob([data], { type: path.extname(file) === ".wav" ? "audio/wav" : "audio/mpeg" }), path.basename(file));
  form.set("model", profile.model);
  form.set("response_format", "json");
  if (language) form.set("language", language);
  const response = await fetch("https://api.openai.com/v1/audio/transcriptions", { method: "POST", headers: { Authorization: `Bearer ${profile.apiKey}` }, body: form, signal: AbortSignal.timeout(120_000) });
  if (!response.ok) throw new Error(`OpenAI transcription failed with HTTP ${response.status}; no transcript inferred`);
  const result: any = await response.json();
  if (typeof result.text !== "string") throw new Error("Provider returned no transcription text");
  return result.text;
};

export async function transcribe(argv: string[], upload: Upload = uploadOpenAI, testProfile?: Profile) {
  const flags = parseFlags(argv, ["json"]);
  for (const key of Object.keys(flags)) if (!["file", "store", "json", "model", "language"].includes(key)) throw new Error(`Unsupported --${key}`);
  if (typeof flags.file !== "string") throw new Error("--file is required");
  const input = await fs.realpath(flags.file);
  const stat = await fs.stat(input);
  if (!stat.isFile() || stat.size > 250 * 1024 * 1024) throw new Error("Audio must be a regular file up to 250 MiB");
  const start = performance.now();
  const info = JSON.parse(await mediaCommand("ffprobe", ["-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name,duration:format=duration", "-of", "json", input]));
  const codec = info.streams?.[0]?.codec_name;
  const duration = Number(info.format?.duration || info.streams?.[0]?.duration);
  if (!codec || !Number.isFinite(duration) || duration <= 0 || duration > 4 * 60 * 60) throw new Error("Audio stream/duration is unavailable or outside the four-hour limit");
  const profile = testProfile || await loadProfile(path.resolve(String(flags.store || defaultStore())), typeof flags.model === "string" ? flags.model : undefined);
  const extension = path.extname(input).toLowerCase();
  const fastPath = ["mp3", "pcm_s16le", "pcm_s24le"].includes(codec) && [".mp3", ".wav"].includes(extension) && stat.size <= maxUploadBytes && duration <= 900;
  const temp = await fs.mkdtemp(path.join(os.tmpdir(), "wacli-transcribe-"));
  try {
    let files = [input];
    if (!fastPath) {
      // Opus, extensionless audio and larger/longer files share one bounded
      // normalization route; the original recording is never overwritten.
      await mediaCommand("ffmpeg", ["-nostdin", "-v", "error", "-i", input, "-map", "0:a:0", "-vn", "-ac", "1", "-ar", "16000", "-c:a", "libmp3lame", "-b:a", "32k", "-f", "segment", "-segment_time", "900", "-reset_timestamps", "1", path.join(temp, "part-%03d.mp3")], Math.min(15 * 60_000, Math.max(60_000, duration * 1000)));
      files = (await fs.readdir(temp)).filter((name) => /^part-\d+\.mp3$/.test(name)).sort().map((name) => path.join(temp, name));
      if (!files.length) throw new Error("Audio normalization produced no chunks");
    }
    const texts: string[] = [];
    for (const file of files) texts.push(await upload(file, profile, typeof flags.language === "string" ? flags.language : undefined));
    const text = texts.join("\n").trim();
    return { ok: true, status: text ? "transcribed" : "silence", text, provider: profile.provider, model: profile.model,
      normalized: !fastPath, inputCodec: codec, chunks: files.length, durationSeconds: duration, elapsedMs: performance.now() - start };
  } finally {
    // Only this operation's generated directory is removed; original and
    // downloaded attachments remain available for later human inspection.
    await fs.rm(temp, { recursive: true, force: true });
  }
}
