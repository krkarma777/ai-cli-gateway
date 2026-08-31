import { spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { constants } from "node:fs";
import {
  chmod,
  lstat,
  mkdir,
  mkdtemp,
  open,
  readFile,
  readdir,
  realpath,
  rename,
  rm,
  writeFile,
} from "node:fs/promises";
import path from "node:path";
import { isDeepStrictEqual } from "node:util";
import { fileURLToPath } from "node:url";

import {
  LAUNCHER_NAME,
  NODE_RANGE,
  PACKAGE_VERSION,
  TARGETS,
} from "./package-config.js";

const VERIFICATION_FAILURE = "npm package verification failed";
const FILE_TYPE_MASK = 0o170000n;
const MAX_NPM_OUTPUT = 1024 * 1024;
const NPM_TIMEOUT_MS = 60_000;
const PACK_OPTION_KEYS = new Set([
  "stagingRoot",
  "tarballRoot",
  "descriptor",
  "version",
  "npmExecutable",
]);
const SOURCE_OPTION_KEYS = new Set(["repositoryRoot", "version"]);
const COMMON_REPOSITORY = Object.freeze({
  type: "git",
  url: "git+https://github.com/krkarma777/ai-cli-gateway.git",
});
const COMMON_PUBLISH_CONFIG = Object.freeze({
  access: "public",
  provenance: true,
  registry: "https://registry.npmjs.org/",
});

function verificationError() {
  return new Error(VERIFICATION_FAILURE);
}

function sameNode(left, right) {
  return (
    left.dev === right.dev &&
    left.ino === right.ino &&
    (left.mode & FILE_TYPE_MASK) === (right.mode & FILE_TYPE_MASK) &&
    left.isSymbolicLink() === right.isSymbolicLink()
  );
}

function sameFile(left, right) {
  return left.size === right.size && left.isFile() && right.isFile() && sameNode(left, right);
}

function ownedByCurrentUser(metadata) {
  return typeof process.getuid !== "function" || metadata.uid === BigInt(process.getuid());
}

async function canonicalDirectory(directory, { privateRoot = false } = {}) {
  if (
    typeof directory !== "string" ||
    !path.isAbsolute(directory) ||
    path.resolve(directory) !== directory
  ) {
    throw verificationError();
  }
  const before = await lstat(directory, { bigint: true });
  if (
    before.isSymbolicLink() ||
    !before.isDirectory() ||
    !ownedByCurrentUser(before) ||
    (privateRoot && process.platform !== "win32" && (before.mode & 0o077n) !== 0n)
  ) {
    throw verificationError();
  }
  if ((await realpath(directory)) !== directory) {
    throw verificationError();
  }
  const after = await lstat(directory, { bigint: true });
  if (!sameNode(before, after) || after.isSymbolicLink() || !after.isDirectory()) {
    throw verificationError();
  }
  return after;
}

async function canonicalExecutable(filename) {
  if (
    typeof filename !== "string" ||
    !path.isAbsolute(filename) ||
    path.resolve(filename) !== filename
  ) {
    throw verificationError();
  }
  const before = await lstat(filename, { bigint: true });
  if (
    before.isSymbolicLink() ||
    !before.isFile() ||
    (process.platform !== "win32" && (before.mode & 0o111n) === 0n) ||
    (await realpath(filename)) !== filename
  ) {
    throw verificationError();
  }
  const after = await lstat(filename, { bigint: true });
  if (!sameFile(before, after) || after.isSymbolicLink()) {
    throw verificationError();
  }
  return { filename, identity: after };
}

async function assertAbsent(filename) {
  try {
    await lstat(filename);
  } catch (error) {
    if (error?.code === "ENOENT") {
      return;
    }
    throw error;
  }
  throw verificationError();
}

function codePointSort(values) {
  return values.sort((left, right) => (left < right ? -1 : left > right ? 1 : 0));
}

function expectedDirectories(files) {
  const directories = new Set();
  for (const filename of files) {
    let directory = path.posix.dirname(filename);
    while (directory !== ".") {
      directories.add(directory);
      directory = path.posix.dirname(directory);
    }
  }
  return codePointSort([...directories]);
}

async function exactTree(root, expectedFiles) {
  const rootBefore = await lstat(root, { bigint: true });
  if (rootBefore.isSymbolicLink() || !rootBefore.isDirectory()) {
    throw verificationError();
  }
  const files = [];
  const directories = [];
  async function visit(directory, prefix = "") {
    const entries = await readdir(directory);
    codePointSort(entries);
    for (const entry of entries) {
      const relative = prefix === "" ? entry : `${prefix}/${entry}`;
      const filename = path.join(directory, entry);
      const metadata = await lstat(filename, { bigint: true });
      if (metadata.isSymbolicLink()) {
        throw verificationError();
      }
      if (metadata.isDirectory()) {
        directories.push(relative);
        await visit(filename, relative);
      } else if (metadata.isFile()) {
        files.push({ path: relative, metadata });
      } else {
        throw verificationError();
      }
    }
  }
  await visit(root);
  const rootAfter = await lstat(root, { bigint: true });
  if (!sameNode(rootBefore, rootAfter) || rootAfter.isSymbolicLink()) {
    throw verificationError();
  }
  if (
    !isDeepStrictEqual(files.map(({ path: filename }) => filename), expectedFiles) ||
    !isDeepStrictEqual(directories, expectedDirectories(expectedFiles))
  ) {
    throw verificationError();
  }
  return new Map(files.map(({ path: filename, metadata }) => [filename, metadata]));
}

async function stableRead(filename) {
  const before = await lstat(filename, { bigint: true });
  if (before.isSymbolicLink() || !before.isFile()) {
    throw verificationError();
  }
  const content = await readFile(filename);
  const after = await lstat(filename, { bigint: true });
  if (!sameFile(before, after) || after.isSymbolicLink()) {
    throw verificationError();
  }
  return { content, metadata: after };
}

function expectedLauncherManifest(version) {
  return {
    name: LAUNCHER_NAME,
    version,
    description: "Run AI CLI Gateway through the matching native binary.",
    license: "MIT",
    type: "module",
    bin: { "ai-cli-gateway": "bin/ai-cli-gateway.js" },
    files: ["bin/ai-cli-gateway.js", "lib/launcher.js", "README.md", "LICENSE"],
    engines: { node: NODE_RANGE },
    optionalDependencies: Object.fromEntries(
      TARGETS.map((target) => [target.packageName, version]),
    ),
    repository: { ...COMMON_REPOSITORY, directory: "npm/launcher" },
    homepage: "https://github.com/krkarma777/ai-cli-gateway#readme",
    bugs: { url: "https://github.com/krkarma777/ai-cli-gateway/issues" },
    publishConfig: { ...COMMON_PUBLISH_CONFIG },
  };
}

function expectedNativeManifest(target, version) {
  return {
    name: target.packageName,
    version,
    description: `Native AI CLI Gateway binary for ${target.key}.`,
    license: "MIT",
    files: [`bin/${target.executable}`, "README.md", "LICENSE"],
    engines: { node: NODE_RANGE },
    os: [target.platform],
    cpu: [target.arch],
    repository: {
      ...COMMON_REPOSITORY,
      directory: `npm/platforms/${target.key}`,
    },
    homepage: "https://github.com/krkarma777/ai-cli-gateway#readme",
    bugs: { url: "https://github.com/krkarma777/ai-cli-gateway/issues" },
    publishConfig: { ...COMMON_PUBLISH_CONFIG },
  };
}

function launcherReadme() {
  return `# AI CLI Gateway

Install the launcher globally:

\`\`\`sh
npm install --global ai-cli-gateway
\`\`\`

The launcher supports these five native targets:

${TARGETS.map((target) => `- \`${target.key}\` via \`${target.packageName}\``).join("\n")}

Node.js \`${NODE_RANGE}\` is required. Installation uses npm's host-specific optional dependencies and performs no lifecycle downloads; the public packages define no lifecycle scripts.
`;
}

function nativeReadme(target) {
  return `# ${target.packageName}

This package contains the native AI CLI Gateway binary for the \`${target.key}\` target (\`GOOS=${target.goos}\`, \`GOARCH=${target.goarch}\`). It is installed through \`ai-cli-gateway\` and must not be installed or used directly.
`;
}

async function parsedManifest(filename, expected) {
  const { content } = await stableRead(filename);
  const manifest = JSON.parse(content.toString("utf8"));
  if (!isDeepStrictEqual(manifest, expected)) {
    throw verificationError();
  }
}

function requireMode(metadata, expected) {
  if (process.platform !== "win32" && Number(metadata.mode & 0o777n) !== expected) {
    throw verificationError();
  }
}

function packageFiles(target) {
  return target === undefined
    ? [
        "LICENSE",
        "README.md",
        "bin/ai-cli-gateway.js",
        "lib/launcher.js",
        "package.json",
      ]
    : ["LICENSE", "README.md", `bin/${target.executable}`, "package.json"];
}

function packageModes(target) {
  const modes = new Map(packageFiles(target).map((filename) => [filename, 0o644]));
  if (target === undefined) {
    modes.set("bin/ai-cli-gateway.js", 0o755);
  } else if (target.platform !== "win32") {
    modes.set(`bin/${target.executable}`, 0o755);
  }
  return modes;
}

async function validatePackageRoot(packageRoot, target, version) {
  const files = packageFiles(target);
  const metadata = await exactTree(packageRoot, files);
  for (const [filename, expectedMode] of packageModes(target)) {
    requireMode(metadata.get(filename), expectedMode);
  }

  await parsedManifest(
    path.join(packageRoot, "package.json"),
    target === undefined
      ? expectedLauncherManifest(version)
      : expectedNativeManifest(target, version),
  );
  const readme = await stableRead(path.join(packageRoot, "README.md"));
  if (
    readme.content.toString("utf8") !==
    (target === undefined ? launcherReadme() : nativeReadme(target))
  ) {
    throw verificationError();
  }
  const license = await stableRead(path.join(packageRoot, "LICENSE"));
  if (license.content.length === 0) {
    throw verificationError();
  }
  if (target === undefined) {
    const entry = await stableRead(path.join(packageRoot, "bin", "ai-cli-gateway.js"));
    if (
      entry.content.toString("utf8") !==
      '#!/usr/bin/env node\nimport { main } from "../lib/launcher.js";\n\nawait main(process.argv.slice(2));\n'
    ) {
      throw verificationError();
    }
    const implementation = await stableRead(path.join(packageRoot, "lib", "launcher.js"));
    if (implementation.content.length === 0) {
      throw verificationError();
    }
  } else {
    const binary = metadata.get(`bin/${target.executable}`);
    if (binary.size === 0n) {
      throw verificationError();
    }
  }
  return metadata;
}

async function validateSourcePackageRoot(packageRoot, target, version) {
  const files =
    target === undefined
      ? [
          "README.md",
          "bin/ai-cli-gateway.js",
          "lib/launcher.js",
          "package.json",
        ]
      : ["README.md", "package.json"];
  const metadata = await exactTree(packageRoot, files);
  for (const file of files) {
    requireMode(
      metadata.get(file),
      target === undefined && file === "bin/ai-cli-gateway.js" ? 0o755 : 0o644,
    );
  }
  await parsedManifest(
    path.join(packageRoot, "package.json"),
    target === undefined
      ? expectedLauncherManifest(version)
      : expectedNativeManifest(target, version),
  );
  const readme = await stableRead(path.join(packageRoot, "README.md"));
  if (
    readme.content.toString("utf8") !==
    (target === undefined ? launcherReadme() : nativeReadme(target))
  ) {
    throw verificationError();
  }
  if (target === undefined) {
    const entry = await stableRead(path.join(packageRoot, "bin", "ai-cli-gateway.js"));
    if (
      entry.content.toString("utf8") !==
      '#!/usr/bin/env node\nimport { main } from "../lib/launcher.js";\n\nawait main(process.argv.slice(2));\n'
    ) {
      throw verificationError();
    }
    const implementation = await stableRead(path.join(packageRoot, "lib", "launcher.js"));
    if (implementation.content.length === 0) {
      throw verificationError();
    }
  }
}

async function validateSource(repositoryRoot, version) {
  const rootIdentity = await canonicalDirectory(repositoryRoot);
  const npmDirectory = path.join(repositoryRoot, "npm");
  const launcherRoot = path.join(npmDirectory, "launcher");
  const platformsRoot = path.join(npmDirectory, "platforms");
  for (const directory of [npmDirectory, launcherRoot, platformsRoot]) {
    const metadata = await lstat(directory, { bigint: true });
    if (metadata.isSymbolicLink() || !metadata.isDirectory()) {
      throw verificationError();
    }
  }

  const platformEntries = await readdir(platformsRoot);
  if (
    !isDeepStrictEqual(
      codePointSort(platformEntries),
      codePointSort(TARGETS.map(({ key }) => key)),
    )
  ) {
    throw verificationError();
  }
  await validateSourcePackageRoot(launcherRoot, undefined, version);
  for (const target of TARGETS) {
    await validateSourcePackageRoot(
      path.join(platformsRoot, target.key),
      target,
      version,
    );
  }

  const sourceLicense = await stableRead(path.join(repositoryRoot, "LICENSE"));
  requireMode(sourceLicense.metadata, 0o644);
  if (sourceLicense.content.length === 0) {
    throw verificationError();
  }
  const rootAfter = await lstat(repositoryRoot, { bigint: true });
  if (!sameNode(rootIdentity, rootAfter) || rootAfter.isSymbolicLink()) {
    throw verificationError();
  }
}

export async function sourceCheck(options) {
  try {
    if (
      options === null ||
      typeof options !== "object" ||
      Array.isArray(options) ||
      !Object.keys(options).every((key) => SOURCE_OPTION_KEYS.has(key)) ||
      Object.keys(options).length !== SOURCE_OPTION_KEYS.size ||
      options.version !== PACKAGE_VERSION
    ) {
      throw verificationError();
    }
    await validateSource(options.repositoryRoot, options.version);
  } catch {
    throw verificationError();
  }
}

async function selectedStagedTargets(stagingRoot) {
  const entries = await readdir(stagingRoot);
  codePointSort(entries);
  if (!entries.includes("launcher")) {
    throw verificationError();
  }
  const nativeEntries = entries.filter((entry) => entry !== "launcher");
  if (nativeEntries.length !== 1 && nativeEntries.length !== TARGETS.length) {
    throw verificationError();
  }
  const selected = TARGETS.filter(({ key }) => nativeEntries.includes(key));
  if (
    selected.length !== nativeEntries.length ||
    entries.length !== nativeEntries.length + 1
  ) {
    throw verificationError();
  }
  return selected;
}

async function defaultNpmInvocation() {
  const nodeDirectory = path.dirname(process.execPath);
  if (process.platform === "win32") {
    const node = await canonicalExecutable(process.execPath);
    const npmCli = path.join(nodeDirectory, "node_modules", "npm", "bin", "npm-cli.js");
    const cli = await canonicalExecutable(npmCli);
    return { command: node, prefixArguments: [cli.filename], auxiliaries: [cli] };
  }

  const sibling = path.join(nodeDirectory, "npm");
  const canonical = await realpath(sibling);
  const npm = await canonicalExecutable(canonical);
  return { command: npm, prefixArguments: [], auxiliaries: [] };
}

async function programmaticNpmInvocation(npmExecutable) {
  const npm = await canonicalExecutable(npmExecutable);
  return { command: npm, prefixArguments: [], auxiliaries: [] };
}

async function createNpmHome(tarballRoot) {
  const parent = path.dirname(tarballRoot);
  const root = await mkdtemp(path.join(parent, ".npm-pack-home-"));
  const identity = await lstat(root, { bigint: true });
  try {
    await chmod(root, 0o700);
    const secured = await lstat(root, { bigint: true });
    if (
      !sameNode(identity, secured) ||
      secured.isSymbolicLink() ||
      !secured.isDirectory() ||
      !ownedByCurrentUser(secured) ||
      (process.platform !== "win32" && (secured.mode & 0o077n) !== 0n)
    ) {
      throw verificationError();
    }
    const temporary = path.join(root, "tmp");
    const cache = path.join(root, "cache");
    const logs = path.join(root, "logs");
    await Promise.all([
      mkdir(temporary, { mode: 0o700 }),
      mkdir(cache, { mode: 0o700 }),
      mkdir(logs, { mode: 0o700 }),
    ]);
    const userConfig = path.join(root, "user.npmrc");
    const globalConfig = path.join(root, "global.npmrc");
    await Promise.all([
      writeFile(userConfig, "", { flag: "wx", mode: 0o600 }),
      writeFile(globalConfig, "", { flag: "wx", mode: 0o600 }),
    ]);
    return {
      cache,
      globalConfig,
      identity: secured,
      logs,
      root,
      temporary,
      userConfig,
    };
  } catch {
    try {
      await removeOwnedDirectory(root, identity);
    } catch {
      // Cleanup failures do not disclose paths or alter the fixed failure.
    }
    throw verificationError();
  }
}

function closedNpmEnvironment(home) {
  return {
    HOME: home.root,
    USERPROFILE: home.root,
    TMPDIR: home.temporary,
    TMP: home.temporary,
    TEMP: home.temporary,
    PATH: path.dirname(process.execPath),
    LANG: "C",
    LC_ALL: "C",
    NO_UPDATE_NOTIFIER: "1",
    NPM_CONFIG_AUDIT: "false",
    NPM_CONFIG_CACHE: home.cache,
    NPM_CONFIG_FUND: "false",
    NPM_CONFIG_GLOBALCONFIG: home.globalConfig,
    NPM_CONFIG_IGNORE_SCRIPTS: "true",
    NPM_CONFIG_LOGS_DIR: home.logs,
    NPM_CONFIG_OFFLINE: "true",
    NPM_CONFIG_UPDATE_NOTIFIER: "false",
    NPM_CONFIG_USERCONFIG: home.userConfig,
  };
}

async function removeOwnedDirectory(root, identity) {
  try {
    const current = await lstat(root, { bigint: true });
    if (sameNode(identity, current) && current.isDirectory() && !current.isSymbolicLink()) {
      await rm(root, { force: true, recursive: true });
    }
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
}

function collectChild(child) {
  return new Promise((resolve, reject) => {
    const stdout = [];
    const stderr = [];
    let stdoutSize = 0;
    let stderrSize = 0;
    let exceeded = false;
    const timeout = setTimeout(() => {
      exceeded = true;
      child.kill("SIGKILL");
    }, NPM_TIMEOUT_MS);

    const collect = (chunks, chunk, currentSize) => {
      if (currentSize + chunk.length > MAX_NPM_OUTPUT) {
        exceeded = true;
        child.kill("SIGKILL");
        return currentSize;
      }
      chunks.push(chunk);
      return currentSize + chunk.length;
    };
    child.stdout.on("data", (chunk) => {
      stdoutSize = collect(stdout, chunk, stdoutSize);
    });
    child.stderr.on("data", (chunk) => {
      stderrSize = collect(stderr, chunk, stderrSize);
    });
    child.once("error", (error) => {
      clearTimeout(timeout);
      reject(error);
    });
    child.once("close", (code, signal) => {
      clearTimeout(timeout);
      if (exceeded) {
        reject(verificationError());
        return;
      }
      resolve({
        code,
        signal,
        stdout: Buffer.concat(stdout, stdoutSize).toString("utf8"),
        stderr: Buffer.concat(stderr, stderrSize).toString("utf8"),
      });
    });
  });
}

async function assertInvocationStable(invocation) {
  const executable = await lstat(invocation.command.filename, { bigint: true });
  if (!sameFile(invocation.command.identity, executable) || executable.isSymbolicLink()) {
    throw verificationError();
  }
  for (const auxiliary of invocation.auxiliaries) {
    const current = await lstat(auxiliary.filename, { bigint: true });
    if (!sameFile(auxiliary.identity, current) || current.isSymbolicLink()) {
      throw verificationError();
    }
  }
}

async function executeNpmPack(invocation, packageRoot, tarballRoot, home) {
  const argumentsAfterPrefix = [
    "pack",
    "--ignore-scripts",
    "--json",
    "--pack-destination",
    tarballRoot,
  ];
  await assertInvocationStable(invocation);
  const child = spawn(
    invocation.command.filename,
    [...invocation.prefixArguments, ...argumentsAfterPrefix],
    {
      cwd: packageRoot,
      env: closedNpmEnvironment(home),
      shell: false,
      stdio: ["ignore", "pipe", "pipe"],
      windowsHide: true,
    },
  );
  const result = await collectChild(child);
  await assertInvocationStable(invocation);
  if (
    result.code !== 0 ||
    result.signal !== null ||
    result.stderr !== ""
  ) {
    throw verificationError();
  }
  let records;
  try {
    records = JSON.parse(result.stdout);
  } catch {
    throw verificationError();
  }
  if (!Array.isArray(records) || records.length !== 1) {
    throw verificationError();
  }
  return records[0];
}

async function hashRegularTarball(filename) {
  const pathBefore = await lstat(filename, { bigint: true });
  if (
    pathBefore.isSymbolicLink() ||
    !pathBefore.isFile() ||
    pathBefore.size <= 0n ||
    pathBefore.size > BigInt(Number.MAX_SAFE_INTEGER) ||
    (await realpath(filename)) !== filename
  ) {
    throw verificationError();
  }

  const flags = constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0);
  const handle = await open(filename, flags);
  try {
    const opened = await handle.stat({ bigint: true });
    if (!sameFile(pathBefore, opened)) {
      throw verificationError();
    }
    const sha1 = createHash("sha1");
    const sha512 = createHash("sha512");
    const buffer = Buffer.allocUnsafe(64 * 1024);
    let position = 0;
    while (position < Number(opened.size)) {
      const { bytesRead } = await handle.read(
        buffer,
        0,
        Math.min(buffer.length, Number(opened.size) - position),
        position,
      );
      if (bytesRead === 0) {
        throw verificationError();
      }
      const chunk = buffer.subarray(0, bytesRead);
      sha1.update(chunk);
      sha512.update(chunk);
      position += bytesRead;
    }
    const openedAfter = await handle.stat({ bigint: true });
    const pathAfter = await lstat(filename, { bigint: true });
    if (!sameFile(opened, openedAfter) || !sameFile(opened, pathAfter)) {
      throw verificationError();
    }
    return {
      integrity: `sha512-${sha512.digest("base64")}`,
      shasum: sha1.digest("hex"),
      size: Number(opened.size),
    };
  } finally {
    await handle.close();
  }
}

function expectedFilename(name, version) {
  return `${name}-${version}.tgz`;
}

function validatePackFiles(value, target, stagedMetadata) {
  const expectedFiles = packageFiles(target);
  const modes = packageModes(target);
  if (!Array.isArray(value) || value.length !== expectedFiles.length) {
    throw verificationError();
  }
  const seen = new Set();
  for (const file of value) {
    if (
      file === null ||
      typeof file !== "object" ||
      Array.isArray(file) ||
      typeof file.path !== "string" ||
      !Number.isSafeInteger(file.mode) ||
      !Number.isSafeInteger(file.size) ||
      file.size < 0 ||
      !modes.has(file.path) ||
      file.mode !== modes.get(file.path) ||
      stagedMetadata.get(file.path)?.size > BigInt(Number.MAX_SAFE_INTEGER) ||
      file.size !== Number(stagedMetadata.get(file.path)?.size) ||
      seen.has(file.path)
    ) {
      throw verificationError();
    }
    seen.add(file.path);
  }
  const sorted = codePointSort([...seen]);
  if (!isDeepStrictEqual(sorted, expectedFiles)) {
    throw verificationError();
  }
  return sorted;
}

async function descriptorForRecord(
  record,
  target,
  version,
  tarballRoot,
  stagedMetadata,
) {
  const name = target?.packageName ?? LAUNCHER_NAME;
  const filename = expectedFilename(name, version);
  const expectedEntryCount = packageFiles(target).length;
  if (
    record === null ||
    typeof record !== "object" ||
    Array.isArray(record) ||
    record.name !== name ||
    record.version !== version ||
    record.filename !== filename ||
    record.entryCount !== expectedEntryCount ||
    !isDeepStrictEqual(record.bundled, [])
  ) {
    throw verificationError();
  }
  const files = validatePackFiles(record.files, target, stagedMetadata);
  const unpackedSize = record.files.reduce((sum, file) => sum + file.size, 0);
  if (!Number.isSafeInteger(unpackedSize) || record.unpackedSize !== unpackedSize) {
    throw verificationError();
  }
  const hashes = await hashRegularTarball(path.join(tarballRoot, filename));
  if (
    record.integrity !== hashes.integrity ||
    record.shasum !== hashes.shasum ||
    record.size !== hashes.size
  ) {
    throw verificationError();
  }
  return {
    name,
    version,
    filename,
    integrity: hashes.integrity,
    shasum: hashes.shasum,
    size: hashes.size,
    files,
  };
}

function samePackageMetadata(before, after) {
  if (before.size !== after.size) {
    return false;
  }
  for (const [filename, beforeFile] of before) {
    const afterFile = after.get(filename);
    if (afterFile === undefined || !sameFile(beforeFile, afterFile)) {
      return false;
    }
  }
  return true;
}

async function exactTarballEntries(tarballRoot, filenames) {
  const entries = codePointSort(await readdir(tarballRoot));
  if (!isDeepStrictEqual(entries, codePointSort([...filenames]))) {
    throw verificationError();
  }
}

async function writeCanonicalDescriptor(descriptor, descriptors) {
  await assertAbsent(descriptor);
  const directory = path.dirname(descriptor);
  let ownedPath;
  let ownedIdentity;
  try {
    let handle;
    for (let attempt = 0; attempt < 8; attempt += 1) {
      const candidate = path.join(
        directory,
        `.packages.json-${randomBytes(12).toString("hex")}.tmp`,
      );
      try {
        handle = await open(
          candidate,
          constants.O_CREAT | constants.O_EXCL | constants.O_WRONLY,
          0o600,
        );
        ownedPath = candidate;
        break;
      } catch (error) {
        if (error?.code !== "EEXIST") {
          throw error;
        }
      }
    }
    if (ownedPath === undefined || handle === undefined) {
      throw verificationError();
    }
    try {
      ownedIdentity = await handle.stat({ bigint: true });
      if (ownedIdentity.isSymbolicLink() || !ownedIdentity.isFile()) {
        throw verificationError();
      }
      await handle.writeFile(`${JSON.stringify(descriptors, null, 2)}\n`, "utf8");
      await handle.chmod(0o644);
      await handle.sync();
    } finally {
      await handle.close();
    }
    const completedIdentity = await lstat(ownedPath, { bigint: true });
    if (
      !sameNode(ownedIdentity, completedIdentity) ||
      completedIdentity.isSymbolicLink() ||
      !completedIdentity.isFile() ||
      (process.platform !== "win32" &&
        Number(completedIdentity.mode & 0o777n) !== 0o644)
    ) {
      throw verificationError();
    }
    ownedIdentity = completedIdentity;
    await assertAbsent(descriptor);
    await rename(ownedPath, descriptor);
    ownedPath = descriptor;
    const finalIdentity = await lstat(descriptor, { bigint: true });
    if (!sameFile(ownedIdentity, finalIdentity) || (await realpath(descriptor)) !== descriptor) {
      throw verificationError();
    }
    ownedPath = undefined;
    ownedIdentity = undefined;
  } catch {
    if (ownedPath !== undefined && ownedIdentity !== undefined) {
      try {
        const current = await lstat(ownedPath, { bigint: true });
        if (sameNode(ownedIdentity, current) && current.isFile() && !current.isSymbolicLink()) {
          await rm(ownedPath, { force: true });
        }
      } catch {
        // Cleanup failures do not disclose paths or alter the fixed failure.
      }
    }
    throw verificationError();
  }
}

async function packAndVerifyWithInvocation(options, suppliedInvocation) {
  let home;
  try {
    if (
      options === null ||
      typeof options !== "object" ||
      Array.isArray(options) ||
      !Object.keys(options).every((key) => PACK_OPTION_KEYS.has(key)) ||
      Object.keys(options).length !== PACK_OPTION_KEYS.size ||
      options.version !== PACKAGE_VERSION ||
      typeof options.descriptor !== "string" ||
      options.descriptor !== path.join(options.tarballRoot, "packages.json")
    ) {
      throw verificationError();
    }

    const stagingIdentity = await canonicalDirectory(options.stagingRoot, {
      privateRoot: true,
    });
    const tarballIdentity = await canonicalDirectory(options.tarballRoot, {
      privateRoot: true,
    });
    await assertAbsent(options.descriptor);
    await exactTarballEntries(options.tarballRoot, []);
    const invocation =
      suppliedInvocation ?? (await programmaticNpmInvocation(options.npmExecutable));
    const targets = await selectedStagedTargets(options.stagingRoot);

    const packages = [
      ...targets.map((target) => ({
        root: path.join(options.stagingRoot, target.key),
        target,
      })),
      { root: path.join(options.stagingRoot, "launcher"), target: undefined },
    ];
    for (const packageRecord of packages) {
      packageRecord.metadata = await validatePackageRoot(
        packageRecord.root,
        packageRecord.target,
        options.version,
      );
    }

    home = await createNpmHome(options.tarballRoot);
    const descriptors = [];
    for (const packageRecord of packages) {
      const name = packageRecord.target?.packageName ?? LAUNCHER_NAME;
      const filename = expectedFilename(name, options.version);
      await assertAbsent(path.join(options.tarballRoot, filename));
      const record = await executeNpmPack(
        invocation,
        packageRecord.root,
        options.tarballRoot,
        home,
      );
      descriptors.push(
        await descriptorForRecord(
          record,
          packageRecord.target,
          options.version,
          options.tarballRoot,
          packageRecord.metadata,
        ),
      );
      await exactTarballEntries(
        options.tarballRoot,
        descriptors.map(({ filename: packedFilename }) => packedFilename),
      );
      const packageMetadataAfter = await validatePackageRoot(
        packageRecord.root,
        packageRecord.target,
        options.version,
      );
      if (!samePackageMetadata(packageRecord.metadata, packageMetadataAfter)) {
        throw verificationError();
      }
    }

    const [stagingAfter, tarballAfter] = await Promise.all([
      lstat(options.stagingRoot, { bigint: true }),
      lstat(options.tarballRoot, { bigint: true }),
    ]);
    if (
      !sameNode(stagingIdentity, stagingAfter) ||
      !sameNode(tarballIdentity, tarballAfter) ||
      stagingAfter.isSymbolicLink() ||
      tarballAfter.isSymbolicLink()
    ) {
      throw verificationError();
    }

    await removeOwnedDirectory(home.root, home.identity);
    home = undefined;
    await writeCanonicalDescriptor(options.descriptor, descriptors);
    await exactTarballEntries(options.tarballRoot, [
      ...descriptors.map(({ filename }) => filename),
      "packages.json",
    ]);
    const [finalStaging, finalTarball] = await Promise.all([
      lstat(options.stagingRoot, { bigint: true }),
      lstat(options.tarballRoot, { bigint: true }),
    ]);
    if (
      !sameNode(stagingIdentity, finalStaging) ||
      !sameNode(tarballIdentity, finalTarball) ||
      finalStaging.isSymbolicLink() ||
      finalTarball.isSymbolicLink()
    ) {
      throw verificationError();
    }
    return descriptors;
  } catch {
    if (home !== undefined) {
      try {
        await removeOwnedDirectory(home.root, home.identity);
      } catch {
        // Cleanup failures do not disclose paths or alter the fixed failure.
      }
    }
    throw verificationError();
  }
}

export async function packAndVerify(options) {
  return packAndVerifyWithInvocation(options);
}

function parseStagingArguments(argv) {
  const allowed = new Set([
    "--staging-root",
    "--tarball-root",
    "--descriptor",
    "--version",
  ]);
  const values = new Map();
  if (argv.length !== 8) {
    throw verificationError();
  }
  for (let index = 0; index < argv.length; index += 2) {
    const option = argv[index];
    const value = argv[index + 1];
    if (
      !allowed.has(option) ||
      values.has(option) ||
      typeof value !== "string" ||
      value.length === 0 ||
      value.startsWith("--")
    ) {
      throw verificationError();
    }
    values.set(option, value);
  }
  if (values.size !== allowed.size) {
    throw verificationError();
  }
  return {
    stagingRoot: values.get("--staging-root"),
    tarballRoot: values.get("--tarball-root"),
    descriptor: values.get("--descriptor"),
    version: values.get("--version"),
  };
}

async function main(argv) {
  if (isDeepStrictEqual(argv, ["--source-check"])) {
    const repositoryRoot = path.resolve(
      path.dirname(fileURLToPath(import.meta.url)),
      "../..",
    );
    await sourceCheck({ repositoryRoot, version: PACKAGE_VERSION });
    return;
  }
  const options = parseStagingArguments(argv);
  const invocation = await defaultNpmInvocation();
  await packAndVerifyWithInvocation(
    {
      ...options,
      npmExecutable: invocation.command.filename,
    },
    invocation,
  );
}

const isMain =
  process.argv[1] !== undefined &&
  fileURLToPath(import.meta.url) === path.resolve(process.argv[1]);
if (isMain) {
  main(process.argv.slice(2)).catch(() => {
    process.stderr.write(`${VERIFICATION_FAILURE}\n`);
    process.exitCode = 1;
  });
}
