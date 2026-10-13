import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";

test("standalone helpers never import or execute the OpenClaw framework", () => {
  const root = path.resolve("agent");
  function walk(dir) {
    for (const item of fs.readdirSync(dir, { withFileTypes: true })) {
      const file = path.join(dir, item.name);
      if (item.isDirectory()) walk(file);
      else if (item.name.endsWith(".ts") && !item.name.endsWith(".test.ts")) {
        const text = fs.readFileSync(file, "utf8");
        assert.doesNotMatch(text, /(?:from\s+["'][^"']*(?:openclaw|\.\.\/\.\.\/\.\.\/src)|(?:execFile|spawn|runCommand)\([^\n]*["']openclaw["'])/i, file);
      }
    }
  }
  walk(root);
});
