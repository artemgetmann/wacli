import os from "node:os";
import path from "node:path";

// The wrapper supplies the exact store it selected. Direct helper use follows
// the core CLI's environment override before falling back to the macOS store.
export function defaultStoreDir(): string {
  const selected = process.env.WACLI_AGENT_STORE?.trim() || process.env.WACLI_STORE_DIR?.trim();
  return selected ? path.resolve(selected) : path.join(os.homedir(), ".wacli");
}

export function isReadOnly(): boolean {
  return ["1", "true", "yes", "on"].includes(
    (process.env.WACLI_READONLY ?? "").trim().toLowerCase(),
  );
}

export function requireWritable(operation: string): void {
  if (isReadOnly()) {
    throw new Error(`read-only mode: ${operation} would modify WhatsApp or the local store`);
  }
}
