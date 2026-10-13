import { execFile } from "node:child_process";
import fs from "node:fs/promises";
import { constants } from "node:fs";
import { createHash, randomUUID } from "node:crypto";
import os from "node:os";
import path from "node:path";
import { defaultStore, parseFlags } from "./runtime.ts";
import { macOSKeychain, validateApiKey, validateReference, type Keychain, type KeychainReference } from "./keychain.ts";

type Profile = { provider: "openai"; model: string; apiKey: string };
type Upload = (file: string, profile: Profile, language?: string) => Promise<string>;
type StoredProfile = { provider: "openai"; model: string; apiKey?: string; keychain?: KeychainReference };
type ConfigurationOptions = { platform?: string; input?: AsyncIterable<Buffer | string>; keychain?: Keychain };
const maxUploadBytes = 25 * 1024 * 1024;
const defaultModel = "gpt-4o-mini-transcribe";

function validateModel(value: unknown): string {
  if (typeof value !== "string" || !/^[a-zA-Z0-9._-]{1,200}$/.test(value)) throw new Error("Invalid transcription model identifier");
  return value;
}

// Opening with NOFOLLOW prevents credential reads through a substituted symlink.
// Invalid JSON errors are sanitized because parser messages can quote the key.
async function readStoredProfile(target: string): Promise<{ profile: StoredProfile; raw: string; inode: number } | undefined> {
  let handle;
  try { handle = await fs.open(target, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK); }
  catch (error: any) {
    if (error.code === "ENOENT") return undefined;
    throw new Error("Transcription profile could not be opened safely");
  }
  try {
    const stat = await handle.stat();
    if (!stat.isFile() || stat.size > 16 * 1024) throw new Error("Transcription profile must be a regular file up to 16 KiB");
    if ((stat.mode & 0o077) !== 0 || (process.getuid && stat.uid !== process.getuid())) throw new Error("Transcription credentials must be owner-only (0600)");
    const raw = await handle.readFile("utf8");
    let profile: StoredProfile;
    try { profile = JSON.parse(raw); } catch { throw new Error("Transcription profile contains invalid JSON; credential details suppressed"); }
    if (!profile || typeof profile !== "object" || profile.provider !== "openai") throw new Error("Unsupported configured provider; no automatic provider switch");
    validateModel(profile.model);
    if (profile.keychain !== undefined) {
      validateReference(profile.keychain);
      if (profile.apiKey !== undefined) throw new Error("Keychain profile must not contain a plaintext API key");
    } else validateApiKey(profile.apiKey);
    return { profile, raw, inode: stat.ino };
  } finally { await handle.close(); }
}

function mediaCommand(binary: string, args: string[], timeout = 60_000): Promise<string> {
  return new Promise((resolve, reject) => execFile(binary, args, { timeout, maxBuffer: 2 * 1024 * 1024 }, (error, stdout, stderr) => {
    if (error) reject(new Error(`${binary} failed (${error.code || "timeout"}): ${String(stderr).slice(-800)}`));
    else resolve(String(stdout));
  }));
}

// Credentials are explicit standalone configuration, not runtime imports from
// another project. Provisioning reads the key from stdin and never returns it.
export async function configureTranscription(argv: string[], options: ConfigurationOptions = {}) {
  if (["1", "true", "yes", "on"].includes((process.env.WACLI_READONLY || "").trim().toLowerCase())) throw new Error("Read-only mode rejects transcription configuration changes");
  const flags = parseFlags(argv, ["json", "migrate-profile"]);
  for (const key of Object.keys(flags)) if (!["store", "model", "json", "credential-store", "keychain-service", "keychain-account", "migrate-profile"].includes(key)) throw new Error(`Unsupported --${key}`);
  const platform = options.platform || process.platform;
  const credentialStore = flags["credential-store"] || (platform === "darwin" ? "keychain" : "profile");
  if (!["keychain", "profile"].includes(String(credentialStore))) throw new Error("--credential-store must be keychain or profile");
  const migrate = flags["migrate-profile"] === true;
  if (migrate && credentialStore !== "keychain") throw new Error("--migrate-profile requires the keychain credential store");
  if (credentialStore === "keychain" && platform !== "darwin") throw new Error("macOS Keychain is unavailable on this platform");
  if (credentialStore === "profile" && (flags["keychain-service"] || flags["keychain-account"])) throw new Error("Keychain references require the keychain credential store");
  const store = path.resolve(String(flags.store || defaultStore()));
  // A stable opaque per-store account avoids coupling unrelated WhatsApp stores
  // while keeping private filesystem paths out of the Keychain item attributes.
  const defaultAccount = `store-${createHash("sha256").update(store).digest("hex").slice(0, 24)}`;
  const reference = credentialStore === "keychain" ? validateReference({ service: flags["keychain-service"] || "wacli.transcription.openai", account: flags["keychain-account"] || defaultAccount }) : undefined;
  if (flags.model !== undefined) validateModel(flags.model);
  const target = path.join(store, "agent-transcription.json");
  await fs.mkdir(store, { recursive: true, mode: 0o700 });
  const lock = path.join(store, ".agent-transcription-config.lock");
  // Serialize our writers before reading credentials or mutating Keychain. A
  // leftover lock needs explicit reconciliation; do not delete another writer's lock.
  try { await fs.mkdir(lock, { mode: 0o700 }); }
  catch { throw new Error("Transcription configuration is already locked; reconcile the existing operation first"); }
  let temp: string | undefined;
  try {
    const existing = await readStoredProfile(target);
    if (migrate && (!existing || existing.profile.keychain)) throw new Error("Migration requires an existing plaintext transcription profile");
    if (!migrate && existing) throw new Error("Transcription profile already exists; use explicit --migrate-profile for Keychain migration");
    const model = validateModel(flags.model || existing?.profile.model || defaultModel);
    const keychain = options.keychain || macOSKeychain(undefined, platform);
    // Never update an item blindly. A same-value item permits recovery after a
    // previous successful creation whose profile replacement did not complete.
    const itemExists = reference ? await keychain.exists(reference) : false;
    if (itemExists && !migrate) throw new Error("Keychain credential already exists; select a different service/account");
    let apiKey: string;
    if (migrate) apiKey = validateApiKey(existing!.profile.apiKey);
    else {
      const chunks: Buffer[] = [];
      let length = 0;
      for await (const chunk of options.input || process.stdin) {
        const data = Buffer.from(chunk);
        length += data.length;
        if (length > 4096) throw new Error("Credential input is too large");
        chunks.push(data);
      }
      apiKey = validateApiKey(Buffer.concat(chunks).toString("utf8").trim());
    }
    if (reference) {
      if (itemExists) {
        if (await keychain.read(reference) !== apiKey) throw new Error("Existing Keychain credential differs; source profile retained");
      } else await keychain.create(reference, apiKey);
      if (await keychain.read(reference) !== apiKey) throw new Error("Keychain verification failed; source profile retained");
    }
    const profile: StoredProfile = { provider: "openai", model, ...(reference ? { keychain: reference } : { apiKey }) };
    if (migrate) {
      temp = path.join(store, `.agent-transcription-${process.pid}-${randomUUID()}.tmp`);
      const handle = await fs.open(temp, "wx", 0o600);
      try { await handle.writeFile(JSON.stringify(profile)); await handle.sync(); } finally { await handle.close(); }
      const staged = await readStoredProfile(temp);
      if (!staged || JSON.stringify(staged.profile) !== JSON.stringify(profile)) throw new Error("Staged Keychain profile could not be verified; source profile retained");
      // Preserve a concurrently changed source even if an unrelated writer does
      // not honor our lock. The plaintext disappears only at the atomic rename.
      const current = await readStoredProfile(target);
      if (!current || current.inode !== existing!.inode || current.raw !== existing!.raw) throw new Error("Source profile changed during migration; plaintext retained");
      await fs.rename(temp, target);
      temp = undefined;
    } else await fs.writeFile(target, JSON.stringify(profile), { mode: 0o600, flag: "wx" });
    const written = await readStoredProfile(target);
    if (!written || JSON.stringify(written.profile) !== JSON.stringify(profile)) throw new Error("Transcription profile write could not be verified");
    return { ok: true, provider: "openai", model, configured: true, credentialStore, migrated: migrate, ...(reference ? { keychain: reference } : {}) };
  } finally {
    if (temp) await fs.rm(temp, { force: true });
    await fs.rmdir(lock);
  }
}

export async function loadProfile(store: string, modelOverride?: string, keychain: Keychain = macOSKeychain()): Promise<Profile> {
  const profile = (await readStoredProfile(path.join(store, "agent-transcription.json")))?.profile;
  const apiKey = process.env.OPENAI_API_KEY || (profile?.keychain ? await keychain.read(profile.keychain) : profile?.apiKey);
  if (!apiKey) throw new Error("Standalone OpenAI credentials are not configured; use agent configure-transcription with the key on stdin");
  return { provider: "openai", apiKey: validateApiKey(apiKey), model: validateModel(modelOverride || profile?.model || defaultModel) };
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
  let response: Response;
  try {
    response = await fetch("https://api.openai.com/v1/audio/transcriptions", { method: "POST", headers: { Authorization: `Bearer ${profile.apiKey}` }, body: form, signal: AbortSignal.timeout(120_000) });
  } catch {
    // Transport error objects can retain request headers. Never surface their
    // messages/causes, and never switch providers after an uncertain upload.
    throw new Error("OpenAI transcription request failed; credential details suppressed");
  }
  if (!response.ok) throw new Error(`OpenAI transcription failed with HTTP ${response.status}; no transcript inferred`);
  let result: any;
  try { result = await response.json(); }
  catch { throw new Error("OpenAI transcription response was invalid; no transcript inferred"); }
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
