import { spawn } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { safeSend } from "./lib/send.ts";
import { transcribe, configureTranscription } from "./lib/transcribe.ts";

const [action, ...args] = process.argv.slice(2);
const root = path.dirname(fileURLToPath(import.meta.url));
try {
  if (action === "send-text" || action === "send-file") {
    const result = await safeSend(action === "send-text" ? "text" : "file", args);
    if (args.includes("--json")) console.log(JSON.stringify(result));
    else {
      console.log(result.message);
      console.log(`verification_status=${result.verification.status}\nread_state_status=${result.readState.status}\nquote_status=${result.quote.status}`);
    }
    if (!result.ok) process.exitCode = 1;
  } else if (action === "configure-transcription") {
    console.log(JSON.stringify(await configureTranscription(args)));
  } else if (action === "transcribe") {
    const result = await transcribe(args);
    console.log(args.includes("--json") ? JSON.stringify(result) : result.text);
  } else {
    const scripts: Record<string, string> = { live: "owner/live.ts", health: "owner/health.ts", "recent-reply": "read/recent-reply.ts", "monitor-check": "read/monitor-check.ts" };
    if (!scripts[action]) throw new Error(`Unknown agent workflow: ${action || "(missing)"}`);
    // Read helpers retain their stable JSON contracts and exit behavior. They
    // execute only packaged siblings, never source from a framework checkout.
    const child = spawn(process.execPath, ["--experimental-strip-types", path.join(root, "lib", scripts[action]), ...args], { stdio: "inherit" });
    process.exitCode = await new Promise<number>((resolve, reject) => { child.once("error", reject); child.once("exit", (code) => resolve(code ?? 1)); });
  }
} catch (error: any) {
  if (args.includes("--json")) console.log(JSON.stringify({ ok: false, status: "failed", error: error.message }));
  else console.error(error.message);
  process.exitCode = 1;
}
