import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const npmRoot = path.dirname(fileURLToPath(new URL("../package.json", import.meta.url)));
const version = "0.2.1";
const targets = [
  ["darwin-x64", "ai-cli-gateway-darwin-x64", "darwin", "x64"],
  ["darwin-arm64", "ai-cli-gateway-darwin-arm64", "darwin", "arm64"],
  ["linux-x64", "ai-cli-gateway-linux-x64", "linux", "x64"],
  ["linux-arm64", "ai-cli-gateway-linux-arm64", "linux", "arm64"],
  ["win32-x64", "ai-cli-gateway-win32-x64", "win32", "x64"],
];

async function manifest(relative) {
  return JSON.parse(await readFile(path.join(npmRoot, relative, "package.json"), "utf8"));
}

test("launcher has five exact native optional dependencies and no scripts", async () => {
  const value = await manifest("launcher");
  assert.equal(value.name, "ai-cli-gateway");
  assert.equal(value.version, version);
  assert.deepEqual(value.bin, { "ai-cli-gateway": "bin/ai-cli-gateway.js" });
  assert.deepEqual(value.engines, { node: ">=22.14.0" });
  assert.equal(Object.hasOwn(value, "scripts"), false);
  assert.deepEqual(
    value.optionalDependencies,
    Object.fromEntries(targets.map(([, name]) => [name, version])),
  );
});

for (const [directory, name, os, cpu] of targets) {
  test(name + " has one exact host constraint", async () => {
    const value = await manifest(path.join("platforms", directory));
    assert.equal(value.name, name);
    assert.equal(value.version, version);
    assert.deepEqual(value.os, [os]);
    assert.deepEqual(value.cpu, [cpu]);
    assert.equal(Object.hasOwn(value, "scripts"), false);
    assert.equal(Object.hasOwn(value, "bin"), false);
    assert.equal(Object.hasOwn(value, "dependencies"), false);
  });
}
