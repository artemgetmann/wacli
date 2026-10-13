import { execFile } from "node:child_process";

export type KeychainReference = { service: string; account: string };
type SecurityResult = { code: number | null; stdout: string };
export type SecurityRunner = (args: string[], input?: string) => Promise<SecurityResult>;
export type Keychain = {
  exists(reference: KeychainReference): Promise<boolean>;
  read(reference: KeychainReference): Promise<string>;
  create(reference: KeychainReference, apiKey: string): Promise<void>;
};

// A deliberately narrow token grammar makes security's interactive parser safe:
// no quoting, shell metacharacters, whitespace or extra commands can enter stdin.
export function validateReference(value: unknown): KeychainReference {
  const reference = value as KeychainReference;
  if (!reference || typeof reference !== "object" ||
      ![reference.service, reference.account].every((part) => typeof part === "string" && /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,199}$/.test(part))) {
    throw new Error("Invalid Keychain service/account reference");
  }
  return { service: reference.service, account: reference.account };
}

export function validateApiKey(value: unknown): string {
  if (typeof value !== "string" || !/^[a-zA-Z0-9_-]{1,4096}$/.test(value)) throw new Error("A single supported OpenAI API key is required");
  return value;
}

// Never use a shell or put a password in argv. Both output streams remain
// private, including security's interactive diagnostics, which may echo input.
const runSecurity: SecurityRunner = (args, input) => new Promise((resolve) => {
  const child = execFile("/usr/bin/security", args, { timeout: 30_000, maxBuffer: 64 * 1024 }, (error, stdout) => {
    resolve({ code: typeof error?.code === "number" ? error.code : error ? null : 0, stdout: String(stdout) });
  });
  child.stdin?.on("error", () => { /* A failed child is reported by its callback, without printing stdin. */ });
  child.stdin?.end(input);
});

export function macOSKeychain(runner: SecurityRunner = runSecurity, platform = process.platform): Keychain {
  const argsFor = (reference: KeychainReference) => {
    if (platform !== "darwin") throw new Error("macOS Keychain is unavailable on this platform");
    const valid = validateReference(reference);
    return ["find-generic-password", "-s", valid.service, "-a", valid.account];
  };
  // Errors from native processes must never escape: an error object can carry
  // the command's secret stdin, stdout or stderr even when its message looks safe.
  const run = async (args: string[], input?: string) => {
    try { return await runner(args, input); }
    catch { throw new Error("macOS Keychain command failed; credential details suppressed"); }
  };
  return {
    async exists(reference) {
      const result = await run(argsFor(reference));
      if (result.code === 44) return false; // errSecItemNotFound, not locked/denied/unavailable.
      if (result.code !== 0) throw new Error("macOS Keychain lookup failed; unlock/access approval may be required");
      return true;
    },
    async read(reference) {
      const result = await run([...argsFor(reference), "-w"]);
      if (result.code === 44) throw new Error("Configured macOS Keychain credential was not found");
      if (result.code !== 0) throw new Error("macOS Keychain credential access failed; unlock/access approval may be required");
      return validateApiKey(result.stdout.trim());
    },
    async create(reference, apiKey) {
      argsFor(reference);
      const valid = validateReference(reference);
      const key = validateApiKey(apiKey);
      // No -U or -A: never overwrite another credential or grant global access.
      // Interactive mode may exit successfully after a failed command, so only
      // exact readback proves creation. A failed readback leaves the profile intact.
      const result = await run(["-i"], `add-generic-password -s ${valid.service} -a ${valid.account} -w ${key}\n`);
      if (result.code !== 0) throw new Error("macOS Keychain credential creation failed; source profile retained");
      const stored = await run([...argsFor(reference), "-w"]);
      if (stored.code !== 0 || stored.stdout.trim() !== key) throw new Error("macOS Keychain credential verification failed; source profile retained");
    },
  };
}
