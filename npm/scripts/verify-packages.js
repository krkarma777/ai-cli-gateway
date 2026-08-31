import { spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { constants } from "node:fs";
import {
  chmod,
  link,
  lstat,
  mkdir,
  mkdtemp,
  open,
  readdir,
  realpath,
  rm,
  unlink,
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
const MIN_NPM_TIMEOUT_MS = 10;
const COMPLETION_MARKER_CONTENT = Buffer.from(
  "ai-cli-gateway npm staging complete\n",
  "utf8",
);
const LAUNCHER_ENTRY_CONTENT = Buffer.from(
  '#!/usr/bin/env node\nimport { main } from "../lib/launcher.js";\n\nawait main(process.argv.slice(2));\n',
  "utf8",
);
const LAUNCHER_IMPLEMENTATION_SHA512 =
  "a547259ed0358f3fe873eaac1144feb499217b068ee4b969dbe0a2e47e6fec1c1185f073001cd4f5af7ef28b531da9c86ff965efd4dbc5f2a8bdc5c54bca1990";
const PACK_OPTION_KEYS = new Set([
  "stagingRoot",
  "tarballRoot",
  "descriptor",
  "version",
  "npmExecutable",
  "npmArguments",
  "npmTimeoutMs",
]);
const REQUIRED_PACK_OPTION_KEYS = new Set([
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
  return (
    left.size === right.size &&
    left.nlink === right.nlink &&
    left.isFile() &&
    right.isFile() &&
    sameNode(left, right)
  );
}

function sameDirectory(left, right) {
  return (
    left.nlink === right.nlink &&
    left.isDirectory() &&
    right.isDirectory() &&
    sameNode(left, right)
  );
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

async function canonicalRegularFile(filename) {
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
    before.nlink !== 1n ||
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
  if (
    rootBefore.isSymbolicLink() ||
    !rootBefore.isDirectory() ||
    !ownedByCurrentUser(rootBefore) ||
    (await realpath(root)) !== root
  ) {
    throw verificationError();
  }
  const files = [];
  const directories = [];
  const directoryMetadata = new Map();
  async function visit(directory, prefix = "") {
    const before = await lstat(directory, { bigint: true });
    if (
      before.isSymbolicLink() ||
      !before.isDirectory() ||
      !ownedByCurrentUser(before)
    ) {
      throw verificationError();
    }
    const entries = await readdir(directory);
    codePointSort(entries);
    for (const entry of entries) {
      const relative = prefix === "" ? entry : `${prefix}/${entry}`;
      const filename = path.join(directory, entry);
      const metadata = await lstat(filename, { bigint: true });
      if (metadata.isSymbolicLink() || !ownedByCurrentUser(metadata)) {
        throw verificationError();
      }
      if (metadata.isDirectory()) {
        directories.push(relative);
        await visit(filename, relative);
      } else if (metadata.isFile()) {
        if (metadata.nlink !== 1n) {
          throw verificationError();
        }
        files.push({ path: relative, metadata });
      } else {
        throw verificationError();
      }
    }
    const after = await lstat(directory, { bigint: true });
    if (
      !sameDirectory(before, after) ||
      after.isSymbolicLink() ||
      !ownedByCurrentUser(after)
    ) {
      throw verificationError();
    }
    directoryMetadata.set(prefix, after);
  }
  await visit(root);
  const rootAfter = await lstat(root, { bigint: true });
  if (
    !sameDirectory(rootBefore, rootAfter) ||
    rootAfter.isSymbolicLink() ||
    !ownedByCurrentUser(rootAfter)
  ) {
    throw verificationError();
  }
  if (
    !isDeepStrictEqual(files.map(({ path: filename }) => filename), expectedFiles) ||
    !isDeepStrictEqual(directories, expectedDirectories(expectedFiles))
  ) {
    throw verificationError();
  }
  return {
    directories: directoryMetadata,
    files: new Map(files.map(({ path: filename, metadata }) => [filename, metadata])),
  };
}

async function stableRead(filename, expectedMetadata) {
  const before = await lstat(filename, { bigint: true });
  if (
    before.isSymbolicLink() ||
    !before.isFile() ||
    before.nlink !== 1n ||
    !ownedByCurrentUser(before) ||
    (expectedMetadata !== undefined && !sameFile(expectedMetadata, before))
  ) {
    throw verificationError();
  }
  const handle = await open(
    filename,
    constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0),
  );
  try {
    const opened = await handle.stat({ bigint: true });
    if (!sameFile(before, opened)) {
      throw verificationError();
    }
    const content = await handle.readFile();
    const [openedAfter, pathAfter] = await Promise.all([
      handle.stat({ bigint: true }),
      lstat(filename, { bigint: true }),
    ]);
    if (
      !sameFile(opened, openedAfter) ||
      !sameFile(opened, pathAfter) ||
      pathAfter.isSymbolicLink() ||
      pathAfter.nlink !== 1n ||
      !ownedByCurrentUser(pathAfter)
    ) {
      throw verificationError();
    }
    return { content, metadata: pathAfter };
  } finally {
    await handle.close();
  }
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

async function parsedManifest(filename, expected, expectedMetadata) {
  const { content } = await stableRead(filename, expectedMetadata);
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
    requireMode(metadata.files.get(filename), expectedMode);
  }

  await parsedManifest(
    path.join(packageRoot, "package.json"),
    target === undefined
      ? expectedLauncherManifest(version)
      : expectedNativeManifest(target, version),
    metadata.files.get("package.json"),
  );
  const readme = await stableRead(
    path.join(packageRoot, "README.md"),
    metadata.files.get("README.md"),
  );
  if (
    readme.content.toString("utf8") !==
    (target === undefined ? launcherReadme() : nativeReadme(target))
  ) {
    throw verificationError();
  }
  const license = await stableRead(
    path.join(packageRoot, "LICENSE"),
    metadata.files.get("LICENSE"),
  );
  if (license.content.length === 0) {
    throw verificationError();
  }
  if (target === undefined) {
    const entry = await stableRead(
      path.join(packageRoot, "bin", "ai-cli-gateway.js"),
      metadata.files.get("bin/ai-cli-gateway.js"),
    );
    if (!entry.content.equals(LAUNCHER_ENTRY_CONTENT)) {
      throw verificationError();
    }
    const implementation = await stableRead(
      path.join(packageRoot, "lib", "launcher.js"),
      metadata.files.get("lib/launcher.js"),
    );
    if (
      createHash("sha512").update(implementation.content).digest("hex") !==
      LAUNCHER_IMPLEMENTATION_SHA512
    ) {
      throw verificationError();
    }
  } else {
    const binary = metadata.files.get(`bin/${target.executable}`);
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
      metadata.files.get(file),
      target === undefined && file === "bin/ai-cli-gateway.js" ? 0o755 : 0o644,
    );
  }
  await parsedManifest(
    path.join(packageRoot, "package.json"),
    target === undefined
      ? expectedLauncherManifest(version)
      : expectedNativeManifest(target, version),
    metadata.files.get("package.json"),
  );
  const readme = await stableRead(
    path.join(packageRoot, "README.md"),
    metadata.files.get("README.md"),
  );
  if (
    readme.content.toString("utf8") !==
    (target === undefined ? launcherReadme() : nativeReadme(target))
  ) {
    throw verificationError();
  }
  if (target === undefined) {
    const entry = await stableRead(
      path.join(packageRoot, "bin", "ai-cli-gateway.js"),
      metadata.files.get("bin/ai-cli-gateway.js"),
    );
    if (!entry.content.equals(LAUNCHER_ENTRY_CONTENT)) {
      throw verificationError();
    }
    const implementation = await stableRead(
      path.join(packageRoot, "lib", "launcher.js"),
      metadata.files.get("lib/launcher.js"),
    );
    if (
      createHash("sha512").update(implementation.content).digest("hex") !==
      LAUNCHER_IMPLEMENTATION_SHA512
    ) {
      throw verificationError();
    }
  }
  return metadata;
}

async function stableOwnedDirectory(directory) {
  const before = await lstat(directory, { bigint: true });
  if (
    before.isSymbolicLink() ||
    !before.isDirectory() ||
    !ownedByCurrentUser(before) ||
    (await realpath(directory)) !== directory
  ) {
    throw verificationError();
  }
  const after = await lstat(directory, { bigint: true });
  if (!sameDirectory(before, after) || !ownedByCurrentUser(after)) {
    throw verificationError();
  }
  return after;
}

async function assertDirectoryStable(directory, expected) {
  const current = await lstat(directory, { bigint: true });
  if (
    !sameDirectory(expected, current) ||
    current.isSymbolicLink() ||
    !ownedByCurrentUser(current)
  ) {
    throw verificationError();
  }
}

async function validateSource(repositoryRoot, version) {
  const rootIdentity = await canonicalDirectory(repositoryRoot);
  const npmDirectory = path.join(repositoryRoot, "npm");
  const launcherRoot = path.join(npmDirectory, "launcher");
  const platformsRoot = path.join(npmDirectory, "platforms");
  const directorySnapshots = new Map();
  for (const directory of [npmDirectory, launcherRoot, platformsRoot]) {
    directorySnapshots.set(directory, await stableOwnedDirectory(directory));
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
  await assertDirectoryStable(platformsRoot, directorySnapshots.get(platformsRoot));
  const packageSnapshots = [
    {
      files: [
        "README.md",
        "bin/ai-cli-gateway.js",
        "lib/launcher.js",
        "package.json",
      ],
      metadata: await validateSourcePackageRoot(launcherRoot, undefined, version),
      root: launcherRoot,
    },
  ];
  for (const target of TARGETS) {
    const packageRoot = path.join(platformsRoot, target.key);
    packageSnapshots.push({
      files: ["README.md", "package.json"],
      metadata: await validateSourcePackageRoot(packageRoot, target, version),
      root: packageRoot,
    });
  }

  const sourceLicense = await stableRead(path.join(repositoryRoot, "LICENSE"));
  requireMode(sourceLicense.metadata, 0o644);
  if (sourceLicense.content.length === 0) {
    throw verificationError();
  }
  await stableRead(
    path.join(repositoryRoot, "LICENSE"),
    sourceLicense.metadata,
  );
  for (const [directory, expected] of directorySnapshots) {
    await assertDirectoryStable(directory, expected);
  }
  for (const snapshot of packageSnapshots) {
    const current = await exactTree(snapshot.root, snapshot.files);
    if (!samePackageMetadata(snapshot.metadata, current)) {
      throw verificationError();
    }
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
  if (!entries.includes("launcher") || !entries.includes(".complete")) {
    throw verificationError();
  }
  const nativeEntries = entries.filter(
    (entry) => entry !== "launcher" && entry !== ".complete",
  );
  if (nativeEntries.length !== 1 && nativeEntries.length !== TARGETS.length) {
    throw verificationError();
  }
  const selected = TARGETS.filter(({ key }) => nativeEntries.includes(key));
  if (
    selected.length !== nativeEntries.length ||
    entries.length !== nativeEntries.length + 2
  ) {
    throw verificationError();
  }
  return selected;
}

async function validateCompletionMarker(stagingRoot, expectedIdentity) {
  const marker = path.join(stagingRoot, ".complete");
  const value = await stableRead(marker, expectedIdentity);
  if (
    !value.content.equals(COMPLETION_MARKER_CONTENT) ||
    (process.platform !== "win32" &&
      Number(value.metadata.mode & 0o777n) !== 0o644)
  ) {
    throw verificationError();
  }
  return value.metadata;
}

async function defaultNpmInvocation() {
  const nodeDirectory = path.dirname(process.execPath);
  if (process.platform === "win32") {
    const node = await canonicalExecutable(process.execPath);
    const npmCli = path.join(nodeDirectory, "node_modules", "npm", "bin", "npm-cli.js");
    const cli = await canonicalRegularFile(npmCli);
    return { command: node, prefixArguments: [cli.filename], auxiliaries: [cli] };
  }

  const sibling = path.join(nodeDirectory, "npm");
  const canonical = await realpath(sibling);
  const npm = await canonicalExecutable(canonical);
  return { command: npm, prefixArguments: [], auxiliaries: [] };
}

async function programmaticNpmInvocation(npmExecutable, npmArguments) {
  const npm = await canonicalExecutable(npmExecutable);
  if (npmArguments === undefined) {
    return { command: npm, prefixArguments: [], auxiliaries: [] };
  }
  if (!Array.isArray(npmArguments) || npmArguments.length !== 1) {
    throw verificationError();
  }
  const fixedScript = await canonicalRegularFile(npmArguments[0]);
  return {
    command: npm,
    prefixArguments: [fixedScript.filename],
    auxiliaries: [fixedScript],
  };
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

function collectChild(child, timeoutMs) {
  return new Promise((resolve, reject) => {
    const stdout = [];
    const stderr = [];
    let stdoutSize = 0;
    let stderrSize = 0;
    let exceeded = false;
    const timeout = setTimeout(() => {
      exceeded = true;
      child.kill("SIGKILL");
    }, timeoutMs);

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

async function executeNpmPack(
  invocation,
  packageRoot,
  tarballRoot,
  home,
  timeoutMs,
) {
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
  const result = await collectChild(child, timeoutMs);
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
    pathBefore.nlink !== 1n ||
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
    if (!sameFile(pathBefore, opened) || opened.nlink !== 1n) {
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
    if (
      !sameFile(opened, openedAfter) ||
      !sameFile(opened, pathAfter) ||
      openedAfter.nlink !== 1n ||
      pathAfter.nlink !== 1n ||
      pathAfter.isSymbolicLink()
    ) {
      throw verificationError();
    }
    return {
      integrity: `sha512-${sha512.digest("base64")}`,
      identity: pathAfter,
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
      stagedMetadata.files.get(file.path)?.size > BigInt(Number.MAX_SAFE_INTEGER) ||
      file.size !== Number(stagedMetadata.files.get(file.path)?.size) ||
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
    descriptor: {
      name,
      version,
      filename,
      integrity: hashes.integrity,
      shasum: hashes.shasum,
      size: hashes.size,
      files,
    },
    tarballIdentity: hashes.identity,
  };
}

function samePackageMetadata(before, after) {
  if (
    before.files.size !== after.files.size ||
    before.directories.size !== after.directories.size
  ) {
    return false;
  }
  for (const [filename, beforeFile] of before.files) {
    const afterFile = after.files.get(filename);
    if (afterFile === undefined || !sameFile(beforeFile, afterFile)) {
      return false;
    }
  }
  for (const [directory, beforeDirectory] of before.directories) {
    const afterDirectory = after.directories.get(directory);
    if (
      afterDirectory === undefined ||
      !sameDirectory(beforeDirectory, afterDirectory)
    ) {
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

function isUnsupportedDirectorySync(error) {
  return ["EBADF", "EINVAL", "EISDIR", "ENOSYS", "ENOTSUP", "EPERM"].includes(
    error?.code,
  );
}

async function syncDirectory(directory) {
  let handle;
  try {
    handle = await open(directory, constants.O_RDONLY);
    await handle.sync();
  } catch (error) {
    if (!isUnsupportedDirectorySync(error)) {
      throw error;
    }
  } finally {
    await handle?.close();
  }
}

async function removeOwnedFile(filename, identity) {
  if (filename === undefined || identity === undefined) {
    return;
  }
  try {
    const current = await lstat(filename, { bigint: true });
    if (sameNode(identity, current) && current.isFile() && !current.isSymbolicLink()) {
      await unlink(filename);
    }
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
}

async function writeCanonicalDescriptor(descriptor, descriptors) {
  await assertAbsent(descriptor);
  const directory = path.dirname(descriptor);
  const serialized = Buffer.from(`${JSON.stringify(descriptors, null, 2)}\n`, "utf8");
  let temporaryPath;
  let temporaryIdentity;
  let descriptorIdentity;
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
        temporaryPath = candidate;
        break;
      } catch (error) {
        if (error?.code !== "EEXIST") {
          throw error;
        }
      }
    }
    if (temporaryPath === undefined || handle === undefined) {
      throw verificationError();
    }
    try {
      temporaryIdentity = await handle.stat({ bigint: true });
      if (
        temporaryIdentity.isSymbolicLink() ||
        !temporaryIdentity.isFile() ||
        temporaryIdentity.nlink !== 1n ||
        !ownedByCurrentUser(temporaryIdentity)
      ) {
        throw verificationError();
      }
      await handle.writeFile(serialized);
      await handle.chmod(0o644);
      await handle.sync();
      const completed = await handle.stat({ bigint: true });
      if (!sameNode(temporaryIdentity, completed) || completed.nlink !== 1n) {
        throw verificationError();
      }
      temporaryIdentity = completed;
    } finally {
      await handle.close();
    }
    const completedIdentity = await lstat(temporaryPath, { bigint: true });
    if (
      !sameFile(temporaryIdentity, completedIdentity) ||
      completedIdentity.isSymbolicLink() ||
      !completedIdentity.isFile() ||
      completedIdentity.nlink !== 1n ||
      !ownedByCurrentUser(completedIdentity) ||
      (process.platform !== "win32" &&
        Number(completedIdentity.mode & 0o777n) !== 0o644)
    ) {
      throw verificationError();
    }
    temporaryIdentity = completedIdentity;
    await link(temporaryPath, descriptor);
    descriptorIdentity = await lstat(descriptor, { bigint: true });
    const linkedTemporary = await lstat(temporaryPath, { bigint: true });
    if (
      !sameNode(temporaryIdentity, descriptorIdentity) ||
      !sameNode(temporaryIdentity, linkedTemporary) ||
      descriptorIdentity.size !== temporaryIdentity.size ||
      descriptorIdentity.nlink !== 2n ||
      linkedTemporary.nlink !== 2n ||
      descriptorIdentity.isSymbolicLink() ||
      !descriptorIdentity.isFile()
    ) {
      throw verificationError();
    }
    await unlink(temporaryPath);
    temporaryPath = undefined;
    temporaryIdentity = undefined;
    const finalIdentity = await lstat(descriptor, { bigint: true });
    if (
      !sameNode(descriptorIdentity, finalIdentity) ||
      finalIdentity.size !== descriptorIdentity.size ||
      finalIdentity.nlink !== 1n ||
      finalIdentity.isSymbolicLink() ||
      !finalIdentity.isFile() ||
      (await realpath(descriptor)) !== descriptor
    ) {
      throw verificationError();
    }
    descriptorIdentity = finalIdentity;
    const finalContent = await stableRead(descriptor, finalIdentity);
    if (!finalContent.content.equals(serialized)) {
      throw verificationError();
    }
    await syncDirectory(directory);
    return descriptorIdentity;
  } catch {
    try {
      await removeOwnedFile(descriptor, descriptorIdentity);
    } catch {
      // Cleanup failures do not disclose paths or alter the fixed failure.
    }
    try {
      await removeOwnedFile(temporaryPath, temporaryIdentity);
    } catch {
      // Cleanup failures do not disclose paths or alter the fixed failure.
    }
    throw verificationError();
  }
}

async function revalidateTarballs(tarballRoot, tarballs) {
  for (const tarball of tarballs) {
    const hashes = await hashRegularTarball(
      path.join(tarballRoot, tarball.descriptor.filename),
    );
    if (
      !sameFile(tarball.identity, hashes.identity) ||
      hashes.integrity !== tarball.descriptor.integrity ||
      hashes.shasum !== tarball.descriptor.shasum ||
      hashes.size !== tarball.descriptor.size
    ) {
      throw verificationError();
    }
  }
}

async function revalidatePackages(packages, version) {
  for (const packageRecord of packages) {
    const current = await validatePackageRoot(
      packageRecord.root,
      packageRecord.target,
      version,
    );
    if (!samePackageMetadata(packageRecord.metadata, current)) {
      throw verificationError();
    }
  }
}

async function packAndVerifyWithInvocation(options, suppliedInvocation) {
  let home;
  let descriptorIdentity;
  try {
    const optionKeys =
      options !== null && typeof options === "object" && !Array.isArray(options)
        ? Object.keys(options)
        : [];
    if (
      options === null ||
      typeof options !== "object" ||
      Array.isArray(options) ||
      !optionKeys.every((key) => PACK_OPTION_KEYS.has(key)) ||
      ![...REQUIRED_PACK_OPTION_KEYS].every((key) => optionKeys.includes(key)) ||
      options.version !== PACKAGE_VERSION ||
      typeof options.descriptor !== "string" ||
      options.descriptor !== path.join(options.tarballRoot, "packages.json") ||
      (options.npmTimeoutMs !== undefined &&
        (!Number.isSafeInteger(options.npmTimeoutMs) ||
          options.npmTimeoutMs < MIN_NPM_TIMEOUT_MS ||
          options.npmTimeoutMs > NPM_TIMEOUT_MS))
    ) {
      throw verificationError();
    }
    const timeoutMs = options.npmTimeoutMs ?? NPM_TIMEOUT_MS;

    const stagingIdentity = await canonicalDirectory(options.stagingRoot, {
      privateRoot: true,
    });
    const tarballIdentity = await canonicalDirectory(options.tarballRoot, {
      privateRoot: true,
    });
    await assertAbsent(options.descriptor);
    await exactTarballEntries(options.tarballRoot, []);
    const invocation =
      suppliedInvocation ??
      (await programmaticNpmInvocation(
        options.npmExecutable,
        options.npmArguments,
      ));
    const completionIdentity = await validateCompletionMarker(options.stagingRoot);
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
    const tarballs = [];
    for (const packageRecord of packages) {
      const name = packageRecord.target?.packageName ?? LAUNCHER_NAME;
      const filename = expectedFilename(name, options.version);
      await assertAbsent(path.join(options.tarballRoot, filename));
      const record = await executeNpmPack(
        invocation,
        packageRecord.root,
        options.tarballRoot,
        home,
        timeoutMs,
      );
      const verified = await descriptorForRecord(
        record,
        packageRecord.target,
        options.version,
        options.tarballRoot,
        packageRecord.metadata,
      );
      descriptors.push(verified.descriptor);
      tarballs.push({
        descriptor: verified.descriptor,
        identity: verified.tarballIdentity,
      });
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

    await revalidateTarballs(options.tarballRoot, tarballs);
    await revalidatePackages(packages, options.version);
    await validateCompletionMarker(options.stagingRoot, completionIdentity);

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
    await revalidateTarballs(options.tarballRoot, tarballs);
    descriptorIdentity = await writeCanonicalDescriptor(
      options.descriptor,
      descriptors,
    );
    await exactTarballEntries(options.tarballRoot, [
      ...descriptors.map(({ filename }) => filename),
      "packages.json",
    ]);
    await revalidateTarballs(options.tarballRoot, tarballs);
    await revalidatePackages(packages, options.version);
    await validateCompletionMarker(options.stagingRoot, completionIdentity);
    const finalDescriptor = await stableRead(
      options.descriptor,
      descriptorIdentity,
    );
    if (
      finalDescriptor.content.toString("utf8") !==
      `${JSON.stringify(descriptors, null, 2)}\n`
    ) {
      throw verificationError();
    }
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
    descriptorIdentity = undefined;
    return descriptors;
  } catch {
    if (home !== undefined) {
      try {
        await removeOwnedDirectory(home.root, home.identity);
      } catch {
        // Cleanup failures do not disclose paths or alter the fixed failure.
      }
    }
    if (descriptorIdentity !== undefined) {
      try {
        await removeOwnedFile(options?.descriptor, descriptorIdentity);
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
