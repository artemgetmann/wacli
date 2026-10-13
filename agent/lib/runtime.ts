import { execFile } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";

export type CommandResult = { ok: boolean; exitCode: number | null; stdout: string; stderr: string; timedOut: boolean };
export type Runner = (args: string[], timeoutMs: number) => Promise<CommandResult>;
export const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));
export const defaultStore = () => process.env.WACLI_AGENT_STORE || process.env.WACLI_STORE_DIR || path.join(os.homedir(), ".wacli");

// Data is passed as argv, never shell code. A timeout after dispatch is uncertain
// even when the CLI child was killed: its owner may still finish the operation.
export const runWacli: Runner = (args, timeoutMs) => new Promise((resolve) => {
  execFile(process.env.WACLI_AGENT_BIN || "wacli", args, { timeout: timeoutMs, maxBuffer: 8 * 1024 * 1024 }, (error, stdout, stderr) => {
    resolve({ ok: !error, exitCode: typeof error?.code === "number" ? error.code : error ? null : 0,
      stdout: String(stdout), stderr: String(stderr), timedOut: !!error?.killed });
  });
});

export function parseFlags(argv: string[], booleanFlags: string[] = ["json", "allow-self", "no-preview"]) {
  const flags: Record<string, string | boolean> = {};
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (!arg.startsWith("--")) throw new Error(`Unexpected argument: ${arg}`);
    const key = arg.slice(2);
    if (key in flags) throw new Error(`Duplicate --${key}`);
    if (booleanFlags.includes(key)) flags[key] = true;
    else {
      const value = argv[++i];
      if (value === undefined || value.startsWith("--")) throw new Error(`--${key} requires a value`);
      flags[key] = value;
    }
  }
  return flags;
}

// Only a complete envelope is trusted; incidental log braces cannot become a
// send receipt. Raw stderr is kept separately by the caller.
export function envelope(stdout: string): any | undefined {
  try { return JSON.parse(stdout.trim()); } catch { return undefined; }
}

export async function exactText(inline: unknown, file: unknown, required: boolean) {
  if (inline !== undefined && file !== undefined) throw new Error("Choose inline text or a text file, not both");
  let text = typeof inline === "string" ? inline : "";
  if (file === "-") {
    const chunks: Buffer[] = [];
    let bytes = 0;
    for await (const chunk of process.stdin) {
      bytes += chunk.length;
      if (bytes > 64 * 1024) throw new Error("Text exceeds the 64 KiB argument limit");
      chunks.push(Buffer.from(chunk));
    }
    text = Buffer.concat(chunks).toString("utf8");
  } else if (typeof file === "string") text = await fs.readFile(file, "utf8");
  if (required && !text.trim()) throw new Error("A nonempty exact message is required");
  if (Buffer.byteLength(text) > 64 * 1024) throw new Error("Text exceeds the 64 KiB argument limit");
  return text;
}
