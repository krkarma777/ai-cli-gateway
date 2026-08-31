import { constants } from "node:fs";
import {
  chmod,
  copyFile,
  lstat,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  realpath,
  rename,
  rm,
} from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { LAUNCHER_NAME, PACKAGE_VERSION, TARGETS } from "./package-config.js";

const STAGING_FAILURE = "npm package staging failed";
const STAGE_OPTION_KEYS = new Set([
  "repositoryRoot",
  "binaryRoot",
  "outputRoot",
  "version",
  "targets",
]);
const FILE_TYPE_MASK = 0o170000n;

function stagingError() {
  return new Error(STAGING_FAILURE);
}

function ownKeysAreKnown(options) {
  return Object.keys(options).every((key) => STAGE_OPTION_KEYS.has(key));
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

function sameCompletedRoot(left, right) {
  return left.size === right.size && left.isDirectory() && right.isDirectory() && sameNode(left, right);
}

function ownedByCurrentUser(metadata) {
  return typeof process.getuid !== "function" || metadata.uid === BigInt(process.getuid());
}

async function existingCanonicalDirectory(directory) {
  if (typeof directory !== "string" || !path.isAbsolute(directory) || path.resolve(directory) !== directory) {
    throw stagingError();
  }
  const before = await lstat(directory, { bigint: true });
  if (before.isSymbolicLink() || !before.isDirectory() || !ownedByCurrentUser(before)) {
    throw stagingError();
  }
  if ((await realpath(directory)) !== directory) {
    throw stagingError();
  }
  const after = await lstat(directory, { bigint: true });
  if (!sameNode(before, after) || after.isSymbolicLink() || !after.isDirectory()) {
    throw stagingError();
  }
  return after;
}

async function exactDirectory(directory, expectedEntries) {
  const metadata = await lstat(directory, { bigint: true });
  if (metadata.isSymbolicLink() || !metadata.isDirectory()) {
    throw stagingError();
  }
  const entries = await readdir(directory);
  if (
    entries.length !== expectedEntries.length ||
    entries.sort().some((entry, index) => entry !== [...expectedEntries].sort()[index])
  ) {
    throw stagingError();
  }
  return metadata;
}

async function regularDirectory(directory) {
  const metadata = await lstat(directory, { bigint: true });
  if (metadata.isSymbolicLink() || !metadata.isDirectory()) {
    throw stagingError();
  }
  return metadata;
}

async function regularSource(filename) {
  const before = await lstat(filename, { bigint: true });
  if (before.isSymbolicLink() || !before.isFile()) {
    throw stagingError();
  }
  return before;
}

async function validatedRead(filename) {
  const before = await regularSource(filename);
  const content = await readFile(filename, "utf8");
  const after = await lstat(filename, { bigint: true });
  if (!sameFile(before, after) || after.isSymbolicLink()) {
    throw stagingError();
  }
  return content;
}

async function validateManifest(filename, name, version) {
  const manifest = JSON.parse(await validatedRead(filename));
  if (
    manifest === null ||
    typeof manifest !== "object" ||
    Array.isArray(manifest) ||
    manifest.name !== name ||
    manifest.version !== version
  ) {
    throw stagingError();
  }
}

async function validateSourceTree(repositoryRoot, version) {
  const npmDirectory = path.join(repositoryRoot, "npm");
  const launcherRoot = path.join(npmDirectory, "launcher");
  const platformsRoot = path.join(npmDirectory, "platforms");

  await regularDirectory(npmDirectory);
  await exactDirectory(launcherRoot, ["README.md", "bin", "lib", "package.json"]);
  await exactDirectory(path.join(launcherRoot, "bin"), ["ai-cli-gateway.js"]);
  await exactDirectory(path.join(launcherRoot, "lib"), ["launcher.js"]);
  await exactDirectory(platformsRoot, TARGETS.map(({ key }) => key));
  await validateManifest(path.join(launcherRoot, "package.json"), LAUNCHER_NAME, version);
  await regularSource(path.join(repositoryRoot, "LICENSE"));
  await regularSource(path.join(launcherRoot, "README.md"));
  await regularSource(path.join(launcherRoot, "bin", "ai-cli-gateway.js"));
  await regularSource(path.join(launcherRoot, "lib", "launcher.js"));

  for (const target of TARGETS) {
    const sourceRoot = path.join(platformsRoot, target.key);
    await exactDirectory(sourceRoot, ["README.md", "package.json"]);
    await regularSource(path.join(sourceRoot, "README.md"));
    await validateManifest(
      path.join(sourceRoot, "package.json"),
      target.packageName,
      version,
    );
  }
}

function selectedTargets(value) {
  const requested = value === undefined ? [...TARGETS] : value;
  if (!Array.isArray(requested) || requested.length === 0 || requested.length > TARGETS.length) {
    throw stagingError();
  }

  const selectedKeys = new Set();
  for (const candidate of requested) {
    const canonical = TARGETS.find((target) => target.key === candidate?.key);
    if (
      canonical === undefined ||
      Object.keys(canonical).some((key) => candidate[key] !== canonical[key]) ||
      Object.keys(candidate).length !== Object.keys(canonical).length ||
      selectedKeys.has(canonical.key)
    ) {
      throw stagingError();
    }
    selectedKeys.add(canonical.key);
  }
  return TARGETS.filter(({ key }) => selectedKeys.has(key));
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
  throw stagingError();
}

async function checkedCopy(source, destination, destinationMode) {
  const before = await regularSource(source);
  await copyFile(source, destination, constants.COPYFILE_EXCL);
  const sourceAfter = await lstat(source, { bigint: true });
  if (!sameFile(before, sourceAfter) || sourceAfter.isSymbolicLink()) {
    throw stagingError();
  }

  await chmod(destination, destinationMode);
  const copied = await lstat(destination, { bigint: true });
  if (
    copied.isSymbolicLink() ||
    !copied.isFile() ||
    copied.size !== before.size ||
    (process.platform !== "win32" &&
      Number(copied.mode & 0o777n) !== destinationMode)
  ) {
    throw stagingError();
  }
}

async function createPrivateDirectory(directory) {
  await mkdir(directory, { mode: 0o700 });
  await chmod(directory, 0o700);
  const metadata = await lstat(directory, { bigint: true });
  if (
    metadata.isSymbolicLink() ||
    !metadata.isDirectory() ||
    (process.platform !== "win32" && Number(metadata.mode & 0o777n) !== 0o700) ||
    !ownedByCurrentUser(metadata)
  ) {
    throw stagingError();
  }
  return metadata;
}

async function stageNative(repositoryRoot, binaryRoot, temporaryRoot, target) {
  const sourceRoot = path.join(repositoryRoot, "npm", "platforms", target.key);
  const binaryDirectory = path.join(binaryRoot, target.stagingDirectory);
  const binaryDirectoryIdentity = await regularDirectory(binaryDirectory);
  const packageRoot = path.join(temporaryRoot, target.key);
  const binRoot = path.join(packageRoot, "bin");
  await createPrivateDirectory(packageRoot);
  await createPrivateDirectory(binRoot);

  await checkedCopy(path.join(repositoryRoot, "LICENSE"), path.join(packageRoot, "LICENSE"), 0o644);
  await checkedCopy(path.join(sourceRoot, "README.md"), path.join(packageRoot, "README.md"), 0o644);
  await checkedCopy(path.join(sourceRoot, "package.json"), path.join(packageRoot, "package.json"), 0o644);
  await checkedCopy(
    path.join(binaryDirectory, target.executable),
    path.join(binRoot, target.executable),
    target.platform === "win32" ? 0o644 : 0o755,
  );
  const binaryDirectoryAfter = await lstat(binaryDirectory, { bigint: true });
  if (!sameNode(binaryDirectoryIdentity, binaryDirectoryAfter)) {
    throw stagingError();
  }
}

async function stageLauncher(repositoryRoot, temporaryRoot) {
  const sourceRoot = path.join(repositoryRoot, "npm", "launcher");
  const packageRoot = path.join(temporaryRoot, "launcher");
  const binRoot = path.join(packageRoot, "bin");
  const libRoot = path.join(packageRoot, "lib");
  await createPrivateDirectory(packageRoot);
  await createPrivateDirectory(binRoot);
  await createPrivateDirectory(libRoot);

  await checkedCopy(path.join(repositoryRoot, "LICENSE"), path.join(packageRoot, "LICENSE"), 0o644);
  await checkedCopy(path.join(sourceRoot, "README.md"), path.join(packageRoot, "README.md"), 0o644);
  await checkedCopy(path.join(sourceRoot, "package.json"), path.join(packageRoot, "package.json"), 0o644);
  await checkedCopy(path.join(sourceRoot, "lib", "launcher.js"), path.join(libRoot, "launcher.js"), 0o644);
  await checkedCopy(
    path.join(sourceRoot, "bin", "ai-cli-gateway.js"),
    path.join(binRoot, "ai-cli-gateway.js"),
    0o755,
  );
}

async function removeIfOwned(ownedPath, ownedIdentity) {
  if (ownedPath === undefined || ownedIdentity === undefined) {
    return;
  }
  try {
    const current = await lstat(ownedPath, { bigint: true });
    if (sameNode(ownedIdentity, current) && current.isDirectory() && !current.isSymbolicLink()) {
      await rm(ownedPath, { force: true, recursive: true });
    }
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
}

export async function stagePackages(options) {
  let ownedPath;
  let ownedIdentity;
  try {
    if (
      options === null ||
      typeof options !== "object" ||
      Array.isArray(options) ||
      !ownKeysAreKnown(options) ||
      options.version !== PACKAGE_VERSION
    ) {
      throw stagingError();
    }

    const targets = selectedTargets(options.targets);
    const repositoryIdentity = await existingCanonicalDirectory(options.repositoryRoot);
    const binaryIdentity = await existingCanonicalDirectory(options.binaryRoot);
    if (
      typeof options.outputRoot !== "string" ||
      !path.isAbsolute(options.outputRoot) ||
      path.resolve(options.outputRoot) !== options.outputRoot
    ) {
      throw stagingError();
    }
    const outputParent = path.dirname(options.outputRoot);
    if (outputParent === options.outputRoot) {
      throw stagingError();
    }
    const outputParentIdentity = await existingCanonicalDirectory(outputParent);
    if (
      process.platform !== "win32" &&
      (outputParentIdentity.mode & 0o002n) !== 0n &&
      (outputParentIdentity.mode & 0o1000n) === 0n
    ) {
      throw stagingError();
    }
    await assertAbsent(options.outputRoot);
    await validateSourceTree(options.repositoryRoot, options.version);

    ownedPath = await mkdtemp(
      path.join(outputParent, `.${path.basename(options.outputRoot)}-`),
    );
    await chmod(ownedPath, 0o700);
    ownedIdentity = await lstat(ownedPath, { bigint: true });
    if (
      ownedIdentity.isSymbolicLink() ||
      !ownedIdentity.isDirectory() ||
      (process.platform !== "win32" &&
        Number(ownedIdentity.mode & 0o777n) !== 0o700) ||
      !ownedByCurrentUser(ownedIdentity)
    ) {
      throw stagingError();
    }

    for (const target of targets) {
      await stageNative(options.repositoryRoot, options.binaryRoot, ownedPath, target);
    }
    await stageLauncher(options.repositoryRoot, ownedPath);

    const [repositoryAfter, binaryAfter, outputParentAfter, completedRoot] =
      await Promise.all([
        lstat(options.repositoryRoot, { bigint: true }),
        lstat(options.binaryRoot, { bigint: true }),
        lstat(outputParent, { bigint: true }),
        lstat(ownedPath, { bigint: true }),
      ]);
    if (
      !sameNode(repositoryIdentity, repositoryAfter) ||
      !sameNode(binaryIdentity, binaryAfter) ||
      !sameNode(outputParentIdentity, outputParentAfter) ||
      !sameNode(ownedIdentity, completedRoot) ||
      completedRoot.isSymbolicLink() ||
      !completedRoot.isDirectory()
    ) {
      throw stagingError();
    }

    await assertAbsent(options.outputRoot);
    await rename(ownedPath, options.outputRoot);
    ownedPath = options.outputRoot;
    ownedIdentity = completedRoot;

    const finalBefore = await lstat(options.outputRoot, { bigint: true });
    if ((await realpath(options.outputRoot)) !== options.outputRoot) {
      throw stagingError();
    }
    const [finalAfter, finalParent] = await Promise.all([
      lstat(options.outputRoot, { bigint: true }),
      lstat(outputParent, { bigint: true }),
    ]);
    if (
      !sameCompletedRoot(completedRoot, finalBefore) ||
      !sameCompletedRoot(completedRoot, finalAfter) ||
      !sameNode(outputParentIdentity, finalParent)
    ) {
      throw stagingError();
    }

    const staged = [
      ...targets.map((target) => ({
        name: target.packageName,
        version: options.version,
        root: path.join(options.outputRoot, target.key),
      })),
      {
        name: LAUNCHER_NAME,
        version: options.version,
        root: path.join(options.outputRoot, "launcher"),
      },
    ];
    ownedPath = undefined;
    ownedIdentity = undefined;
    return staged;
  } catch {
    try {
      await removeIfOwned(ownedPath, ownedIdentity);
    } catch {
      // Cleanup failures do not disclose a path or change the public failure.
    }
    throw stagingError();
  }
}

function parseArguments(argv) {
  const values = new Map();
  const allowed = new Set([
    "--repository-root",
    "--binary-root",
    "--output-root",
    "--version",
    "--target",
  ]);
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
      throw stagingError();
    }
    values.set(option, value);
  }
  if (
    argv.length % 2 !== 0 ||
    !values.has("--repository-root") ||
    !values.has("--binary-root") ||
    !values.has("--output-root") ||
    !values.has("--version")
  ) {
    throw stagingError();
  }

  let targets;
  if (values.has("--target")) {
    const target = TARGETS.find(({ key }) => key === values.get("--target"));
    if (target === undefined) {
      throw stagingError();
    }
    targets = [target];
  }
  return {
    repositoryRoot: values.get("--repository-root"),
    binaryRoot: values.get("--binary-root"),
    outputRoot: values.get("--output-root"),
    version: values.get("--version"),
    ...(targets === undefined ? {} : { targets }),
  };
}

async function main(argv) {
  await stagePackages(parseArguments(argv));
}

const isMain =
  process.argv[1] !== undefined &&
  fileURLToPath(import.meta.url) === path.resolve(process.argv[1]);
if (isMain) {
  main(process.argv.slice(2)).catch(() => {
    process.stderr.write(`${STAGING_FAILURE}\n`);
    process.exitCode = 1;
  });
}
