import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const npmRoot = path.dirname(fileURLToPath(new URL("../package.json", import.meta.url)));
const version = "0.2.1";
const targets = [
  ["darwin-x64", "@krkarma777/ai-cli-gateway-darwin-x64", "darwin", "x64"],
  ["darwin-arm64", "@krkarma777/ai-cli-gateway-darwin-arm64", "darwin", "arm64"],
  ["linux-x64", "@krkarma777/ai-cli-gateway-linux-x64", "linux", "x64"],
  ["linux-arm64", "@krkarma777/ai-cli-gateway-linux-arm64", "linux", "arm64"],
  ["win32-x64", "@krkarma777/ai-cli-gateway-win32-x64", "win32", "x64"],
];
const launcherDescription =
  "Build AI MVPs with Codex CLI, Claude Code, and Gemini CLI through a local OpenAI Responses-compatible API.";
const launcherKeywords = [
  "ai",
  "ai-cli",
  "ai-gateway",
  "llm-gateway",
  "openai",
  "openai-compatible",
  "responses-api",
  "codex-cli",
  "claude-code",
  "gemini-cli",
  "local-ai",
  "ai-mvp",
  "structured-output",
  "json-schema",
];
const platformCopy = new Map([
  ["darwin-x64", {
    description: "Internal macOS Intel binary for AI CLI Gateway. Install the ai-cli-gateway package instead.",
    keywords: ["ai-cli-gateway", "native-binary", "darwin", "x64"],
    label: "macOS Intel",
  }],
  ["darwin-arm64", {
    description: "Internal macOS Apple silicon binary for AI CLI Gateway. Install the ai-cli-gateway package instead.",
    keywords: ["ai-cli-gateway", "native-binary", "darwin", "arm64"],
    label: "macOS Apple silicon",
  }],
  ["linux-x64", {
    description: "Internal Linux x86-64 binary for AI CLI Gateway. Install the ai-cli-gateway package instead.",
    keywords: ["ai-cli-gateway", "native-binary", "linux", "x64"],
    label: "Linux x86-64",
  }],
  ["linux-arm64", {
    description: "Internal Linux ARM64 binary for AI CLI Gateway. Install the ai-cli-gateway package instead.",
    keywords: ["ai-cli-gateway", "native-binary", "linux", "arm64"],
    label: "Linux ARM64",
  }],
  ["win32-x64", {
    description: "Internal Windows x86-64 binary for AI CLI Gateway. Install the ai-cli-gateway package instead.",
    keywords: ["ai-cli-gateway", "native-binary", "win32", "x64"],
    label: "Windows x86-64",
  }],
]);

async function manifest(relative) {
  return JSON.parse(await readFile(path.join(npmRoot, relative, "package.json"), "utf8"));
}

async function packageText(relative) {
  return readFile(path.join(npmRoot, relative), "utf8");
}

test("packaged repository license is Apache-2.0 across all manifests", async () => {
  const repositoryLicense = await readFile(path.join(npmRoot, "..", "LICENSE"), "utf8");
  assert.match(
    repositoryLicense,
    /^\s*Apache License\s*\n\s*Version 2\.0, January 2004\s*\n\s*http:\/\/www\.apache\.org\/licenses\//,
  );
  assert.match(repositoryLicense, /TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION/);
  assert.match(repositoryLicense, /END OF TERMS AND CONDITIONS/);

  const packageManifests = [
    await manifest("launcher"),
    ...await Promise.all(
      targets.map(([directory]) => manifest(path.join("platforms", directory))),
    ),
  ];
  const declarations = packageManifests.map(({ name, license }) => ({ name, license }));
  const expected = packageManifests.map(({ name }) => ({ name, license: "Apache-2.0" }));
  assert.deepEqual(
    declarations,
    expected,
    `npm package SPDX declarations:\n${declarations
      .map(({ name, license }) => `${name}: ${String(license)}`)
      .join("\n")}`,
  );
});

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

test("launcher metadata exposes the exact product description and search terms", async () => {
  const value = await manifest("launcher");
  assert.equal(value.description, launcherDescription);
  assert.deepEqual(value.keywords, launcherKeywords);
});

test("launcher README explains the product, first run, SDK boundary, and exclusions", async () => {
  const readme = await packageText("launcher/README.md");
  for (const required of [
    launcherDescription,
    "focused Responses API-compatible subset",
    "https://img.shields.io/npm/v/ai-cli-gateway",
    "https://img.shields.io/npm/dm/ai-cli-gateway",
    "https://img.shields.io/node/v/ai-cli-gateway",
    "actions/workflows/ci.yml/badge.svg?branch=main",
    "https://img.shields.io/npm/l/ai-cli-gateway",
    "npm install --global ai-cli-gateway",
    "ai-cli-gateway init",
    "ai-cli-gateway serve",
    'baseURL: "http://127.0.0.1:8080/v1"',
    "Codex CLI",
    "Claude Code",
    "Gemini CLI",
    "Windows x86-64",
    "POST /v1/responses",
    "GET /v1/models",
    "SSE streaming",
    "tool-call round trips",
    "manually published",
    "do not expose npm provenance attestations",
    "Trusted Publishing",
  ]) {
    assert.ok(readme.includes(required), `launcher README missing ${required}`);
  }
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

for (const [directory, name, os, cpu] of targets) {
  test(`${name} has exact internal-package copy`, async () => {
    const expected = platformCopy.get(directory);
    assert.ok(expected);
    const value = await manifest(path.join("platforms", directory));
    assert.equal(value.description, expected.description);
    assert.deepEqual(value.keywords, expected.keywords);

    const readme = await packageText(path.join("platforms", directory, "README.md"));
    assert.ok(readme.includes("Internal platform package"));
    assert.ok(readme.includes("npm install --global ai-cli-gateway"));
    assert.ok(readme.includes(expected.label));
    assert.ok(readme.includes(`npm os=${os}`));
    assert.ok(readme.includes(`npm cpu=${cpu}`));
    assert.ok(readme.includes("No standalone JavaScript API"));
    assert.ok(readme.includes("https://www.npmjs.com/package/ai-cli-gateway"));
  });
}
