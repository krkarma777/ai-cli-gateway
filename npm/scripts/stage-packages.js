import {
  closeSync,
  constants,
  fchmodSync,
  fstatSync,
  fsyncSync,
  lstatSync,
  openSync,
  writeSync,
} from "node:fs";
import { createHash } from "node:crypto";
import {
  chmod,
  copyFile,
  lstat,
  mkdir,
  open,
  readFile,
  readdir,
  realpath,
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
const COMPLETION_MARKER_CONTENT = Buffer.from(
  "ai-cli-gateway npm staging complete\n",
  "utf8",
);
const LAUNCHER_ENTRY_CONTENT = Buffer.from(
  '#!/usr/bin/env node\nimport { main } from "../lib/launcher.js";\n\nawait main(process.argv.slice(2));\n',
  "utf8",
);
const LAUNCHER_IMPLEMENTATION_SHA512 =
  "d61fd93466ac3c55b636301e58dbf4c79197419afd8a918e31e8b47087625d7fda874ac9d47e44392b4666ada0f2fadb6992e6b5cafd79102710ae4451ea89e1";

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

function privateOutputParent(metadata) {
  return (
    metadata.isDirectory() &&
    !metadata.isSymbolicLink() &&
    ownedByCurrentUser(metadata) &&
    (process.platform === "win32" || (metadata.mode & 0o022n) === 0n)
  );
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
  const before = await lstat(directory, { bigint: true });
  if (
    before.isSymbolicLink() ||
    !before.isDirectory() ||
    !ownedByCurrentUser(before) ||
    (await realpath(directory)) !== directory
  ) {
    throw stagingError();
  }
  const entries = await readdir(directory);
  if (
    entries.length !== expectedEntries.length ||
    entries.sort().some((entry, index) => entry !== [...expectedEntries].sort()[index])
  ) {
    throw stagingError();
  }
  const after = await lstat(directory, { bigint: true });
  if (!sameDirectory(before, after) || !ownedByCurrentUser(after)) {
    throw stagingError();
  }
  return after;
}

async function regularDirectory(directory) {
  const before = await lstat(directory, { bigint: true });
  if (
    before.isSymbolicLink() ||
    !before.isDirectory() ||
    !ownedByCurrentUser(before) ||
    (await realpath(directory)) !== directory
  ) {
    throw stagingError();
  }
  const after = await lstat(directory, { bigint: true });
  if (!sameDirectory(before, after) || !ownedByCurrentUser(after)) {
    throw stagingError();
  }
  return after;
}

async function regularSource(filename) {
  const before = await lstat(filename, { bigint: true });
  if (
    before.isSymbolicLink() ||
    !before.isFile() ||
    before.nlink !== 1n ||
    !ownedByCurrentUser(before)
  ) {
    throw stagingError();
  }
  return before;
}

async function validatedRead(filename) {
  const before = await regularSource(filename);
  const content = await readFile(filename);
  const after = await lstat(filename, { bigint: true });
  if (
    !sameFile(before, after) ||
    after.isSymbolicLink() ||
    after.nlink !== 1n ||
    !ownedByCurrentUser(after)
  ) {
    throw stagingError();
  }
  return { content, metadata: after };
}

async function validatedRegularSource(filename, { nonempty = false } = {}) {
  const source = await validatedRead(filename);
  if (nonempty && source.content.length === 0) {
    throw stagingError();
  }
  return source;
}

async function validateManifest(filename, name, version) {
  const source = await validatedRead(filename);
  const manifest = JSON.parse(source.content.toString("utf8"));
  if (
    manifest === null ||
    typeof manifest !== "object" ||
    Array.isArray(manifest) ||
    manifest.name !== name ||
    manifest.version !== version
  ) {
    throw stagingError();
  }
  return source;
}

async function validateSourceTree(repositoryRoot, version) {
  const npmDirectory = path.join(repositoryRoot, "npm");
  const launcherRoot = path.join(npmDirectory, "launcher");
  const platformsRoot = path.join(npmDirectory, "platforms");

  const plan = { directories: new Map(), files: new Map() };
  const rememberDirectory = async (directory, entries) => {
    const metadata =
      entries === undefined
        ? await regularDirectory(directory)
        : await exactDirectory(directory, entries);
    plan.directories.set(directory, metadata);
  };
  const rememberFile = async (filename, source) => {
    const validated = source ?? (await validatedRead(filename));
    plan.files.set(filename, validated);
    return validated;
  };

  await rememberDirectory(npmDirectory);
  await rememberDirectory(launcherRoot, ["README.md", "bin", "lib", "package.json"]);
  await rememberDirectory(path.join(launcherRoot, "bin"), ["ai-cli-gateway.js"]);
  await rememberDirectory(path.join(launcherRoot, "lib"), ["launcher.js"]);
  await rememberDirectory(platformsRoot, TARGETS.map(({ key }) => key));
  await rememberFile(
    path.join(launcherRoot, "package.json"),
    await validateManifest(
      path.join(launcherRoot, "package.json"),
      LAUNCHER_NAME,
      version,
    ),
  );
  await rememberFile(path.join(repositoryRoot, "LICENSE"));
  await rememberFile(path.join(launcherRoot, "README.md"));
  const entry = await rememberFile(
    path.join(launcherRoot, "bin", "ai-cli-gateway.js"),
  );
  if (!entry.content.equals(LAUNCHER_ENTRY_CONTENT)) {
    throw stagingError();
  }
  const implementation = await rememberFile(
    path.join(launcherRoot, "lib", "launcher.js"),
  );
  if (
    createHash("sha512").update(implementation.content).digest("hex") !==
    LAUNCHER_IMPLEMENTATION_SHA512
  ) {
    throw stagingError();
  }

  for (const target of TARGETS) {
    const sourceRoot = path.join(platformsRoot, target.key);
    await rememberDirectory(sourceRoot, ["README.md", "package.json"]);
    await rememberFile(path.join(sourceRoot, "README.md"));
    await rememberFile(
      path.join(sourceRoot, "package.json"),
      await validateManifest(
        path.join(sourceRoot, "package.json"),
        target.packageName,
        version,
      ),
    );
  }
  return plan;
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

async function validateBinaryTree(binaryRoot, targets) {
  const plan = { directories: new Map(), files: new Map() };
  for (const target of targets) {
    const directory = path.join(binaryRoot, target.stagingDirectory);
    plan.directories.set(directory, await regularDirectory(directory));
    const filename = path.join(directory, target.executable);
    plan.files.set(
      filename,
      await validatedRegularSource(filename, { nonempty: true }),
    );
  }
  return plan;
}

async function assertPlanStable(plan) {
  for (const [directory, expected] of plan.directories) {
    const current = await lstat(directory, { bigint: true });
    if (
      !sameDirectory(expected, current) ||
      current.isSymbolicLink() ||
      !ownedByCurrentUser(current)
    ) {
      throw stagingError();
    }
  }
  for (const [filename, expected] of plan.files) {
    const current = await lstat(filename, { bigint: true });
    if (
      !sameFile(expected.metadata, current) ||
      current.isSymbolicLink() ||
      current.nlink !== 1n ||
      !ownedByCurrentUser(current)
    ) {
      throw stagingError();
    }
  }
}

async function assertPlanPathStable(plan, filename) {
  const source = plan.files.get(filename);
  if (source === undefined) {
    throw stagingError();
  }
  const current = await lstat(filename, { bigint: true });
  if (
    !sameFile(source.metadata, current) ||
    current.isSymbolicLink() ||
    current.nlink !== 1n ||
    !ownedByCurrentUser(current)
  ) {
    throw stagingError();
  }
  for (const [directory, expected] of plan.directories) {
    if (filename !== directory && !filename.startsWith(`${directory}${path.sep}`)) {
      continue;
    }
    const directoryAfter = await lstat(directory, { bigint: true });
    if (
      !sameDirectory(expected, directoryAfter) ||
      directoryAfter.isSymbolicLink() ||
      !ownedByCurrentUser(directoryAfter)
    ) {
      throw stagingError();
    }
  }
  return source;
}

async function checkedCopy(plan, source, destination, destinationMode) {
  const expected = await assertPlanPathStable(plan, source);
  await copyFile(source, destination, constants.COPYFILE_EXCL);
  await assertPlanPathStable(plan, source);

  await chmod(destination, destinationMode);
  const copied = await lstat(destination, { bigint: true });
  if (
    copied.isSymbolicLink() ||
    !copied.isFile() ||
    copied.nlink !== 1n ||
    copied.size !== expected.metadata.size ||
    !ownedByCurrentUser(copied) ||
    (process.platform !== "win32" &&
      Number(copied.mode & 0o777n) !== destinationMode)
  ) {
    throw stagingError();
  }
  const copiedContent = await validatedRead(destination);
  if (
    !sameFile(copied, copiedContent.metadata) ||
    !copiedContent.content.equals(expected.content)
  ) {
    throw stagingError();
  }
  return {
    content: Buffer.from(expected.content),
    metadata: copiedContent.metadata,
    mode: destinationMode,
  };
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

async function stageNative(
  repositoryRoot,
  binaryRoot,
  temporaryRoot,
  target,
  sourcePlan,
  binaryPlan,
) {
  const sourceRoot = path.join(repositoryRoot, "npm", "platforms", target.key);
  const binaryDirectory = path.join(binaryRoot, target.stagingDirectory);
  const packageRoot = path.join(temporaryRoot, target.key);
  const binRoot = path.join(packageRoot, "bin");
  const packageIdentity = await createPrivateDirectory(packageRoot);
  const binIdentity = await createPrivateDirectory(binRoot);
  const files = new Map();

  files.set(
    "LICENSE",
    await checkedCopy(
      sourcePlan,
      path.join(repositoryRoot, "LICENSE"),
      path.join(packageRoot, "LICENSE"),
      0o644,
    ),
  );
  files.set(
    "README.md",
    await checkedCopy(
      sourcePlan,
      path.join(sourceRoot, "README.md"),
      path.join(packageRoot, "README.md"),
      0o644,
    ),
  );
  files.set(
    "package.json",
    await checkedCopy(
      sourcePlan,
      path.join(sourceRoot, "package.json"),
      path.join(packageRoot, "package.json"),
      0o644,
    ),
  );
  files.set(
    `bin/${target.executable}`,
    await checkedCopy(
      binaryPlan,
      path.join(binaryDirectory, target.executable),
      path.join(binRoot, target.executable),
      target.platform === "win32" ? 0o644 : 0o755,
    ),
  );
  await assertPlanStable(sourcePlan);
  await assertPlanStable(binaryPlan);
  const [packageAfter, binAfter, packageEntries, binEntries] = await Promise.all([
    lstat(packageRoot, { bigint: true }),
    lstat(binRoot, { bigint: true }),
    readdir(packageRoot),
    readdir(binRoot),
  ]);
  if (
    !sameNode(packageIdentity, packageAfter) ||
    !sameNode(binIdentity, binAfter) ||
    !packageAfter.isDirectory() ||
    !binAfter.isDirectory() ||
    packageAfter.isSymbolicLink() ||
    packageEntries.sort().join("\0") !== ["LICENSE", "README.md", "bin", "package.json"].sort().join("\0") ||
    binEntries.length !== 1 ||
    binEntries[0] !== target.executable
  ) {
    throw stagingError();
  }
  return {
    directories: new Map([
      ["", packageAfter],
      ["bin", binAfter],
    ]),
    files,
    identity: packageAfter,
    name: target.key,
    root: packageRoot,
  };
}

async function stageLauncher(repositoryRoot, temporaryRoot, sourcePlan) {
  const sourceRoot = path.join(repositoryRoot, "npm", "launcher");
  const packageRoot = path.join(temporaryRoot, "launcher");
  const binRoot = path.join(packageRoot, "bin");
  const libRoot = path.join(packageRoot, "lib");
  const packageIdentity = await createPrivateDirectory(packageRoot);
  const binIdentity = await createPrivateDirectory(binRoot);
  const libIdentity = await createPrivateDirectory(libRoot);
  const files = new Map();

  files.set(
    "LICENSE",
    await checkedCopy(
      sourcePlan,
      path.join(repositoryRoot, "LICENSE"),
      path.join(packageRoot, "LICENSE"),
      0o644,
    ),
  );
  files.set(
    "README.md",
    await checkedCopy(
      sourcePlan,
      path.join(sourceRoot, "README.md"),
      path.join(packageRoot, "README.md"),
      0o644,
    ),
  );
  files.set(
    "package.json",
    await checkedCopy(
      sourcePlan,
      path.join(sourceRoot, "package.json"),
      path.join(packageRoot, "package.json"),
      0o644,
    ),
  );
  files.set(
    "lib/launcher.js",
    await checkedCopy(
      sourcePlan,
      path.join(sourceRoot, "lib", "launcher.js"),
      path.join(libRoot, "launcher.js"),
      0o644,
    ),
  );
  files.set(
    "bin/ai-cli-gateway.js",
    await checkedCopy(
      sourcePlan,
      path.join(sourceRoot, "bin", "ai-cli-gateway.js"),
      path.join(binRoot, "ai-cli-gateway.js"),
      0o755,
    ),
  );
  await assertPlanStable(sourcePlan);
  const [packageAfter, binAfter, libAfter, packageEntries, binEntries, libEntries] =
    await Promise.all([
      lstat(packageRoot, { bigint: true }),
      lstat(binRoot, { bigint: true }),
      lstat(libRoot, { bigint: true }),
      readdir(packageRoot),
      readdir(binRoot),
      readdir(libRoot),
    ]);
  if (
    !sameNode(packageIdentity, packageAfter) ||
    !sameNode(binIdentity, binAfter) ||
    !sameNode(libIdentity, libAfter) ||
    !packageAfter.isDirectory() ||
    !binAfter.isDirectory() ||
    !libAfter.isDirectory() ||
    packageAfter.isSymbolicLink() ||
    packageEntries.sort().join("\0") !== ["LICENSE", "README.md", "bin", "lib", "package.json"].sort().join("\0") ||
    binEntries.length !== 1 ||
    binEntries[0] !== "ai-cli-gateway.js" ||
    libEntries.length !== 1 ||
    libEntries[0] !== "launcher.js"
  ) {
    throw stagingError();
  }
  return {
    directories: new Map([
      ["", packageAfter],
      ["bin", binAfter],
      ["lib", libAfter],
    ]),
    files,
    identity: packageAfter,
    name: "launcher",
    root: packageRoot,
  };
}

function isUnsupportedDirectorySync(error) {
  return ["EBADF", "EINVAL", "EISDIR", "ENOSYS", "ENOTSUP", "EPERM"].includes(
    error?.code,
  );
}

async function syncDirectory(directory, expectedIdentity) {
  let handle;
  try {
    handle = await open(directory, constants.O_RDONLY);
    const opened = await handle.stat({ bigint: true });
    if (
      opened.isSymbolicLink() ||
      !opened.isDirectory() ||
      !ownedByCurrentUser(opened) ||
      (expectedIdentity !== undefined && !sameNode(expectedIdentity, opened))
    ) {
      throw stagingError();
    }
    await handle.sync();
    const openedAfter = await handle.stat({ bigint: true });
    if (!sameNode(opened, openedAfter) || !openedAfter.isDirectory()) {
      throw stagingError();
    }
  } catch (error) {
    if (!isUnsupportedDirectorySync(error)) {
      throw error;
    }
  } finally {
    await handle?.close();
  }
  const pathAfter = await lstat(directory, { bigint: true });
  if (
    pathAfter.isSymbolicLink() ||
    !pathAfter.isDirectory() ||
    !ownedByCurrentUser(pathAfter) ||
    (expectedIdentity !== undefined && !sameNode(expectedIdentity, pathAfter))
  ) {
    throw stagingError();
  }
}

async function exactEntries(directory, expectedEntries) {
  const actual = (await readdir(directory)).sort();
  const expected = [...expectedEntries].sort();
  if (
    actual.length !== expected.length ||
    actual.some((entry, index) => entry !== expected[index])
  ) {
    throw stagingError();
  }
}

async function assertOwnedRoot(root, identity) {
  const current = await lstat(root, { bigint: true });
  if (
    !sameNode(identity, current) ||
    current.isSymbolicLink() ||
    !current.isDirectory() ||
    !ownedByCurrentUser(current)
  ) {
    throw stagingError();
  }
  return current;
}

function stagedPath(root, relative) {
  return relative === "" ? root : path.join(root, ...relative.split("/"));
}

function expectedChildEntries(packageRecord, prefix) {
  const entries = new Set();
  const rememberChild = (relative) => {
    if (relative === prefix) {
      return;
    }
    const parent = path.posix.dirname(relative);
    if ((parent === "." ? "" : parent) === prefix) {
      entries.add(path.posix.basename(relative));
    }
  };
  for (const relative of packageRecord.directories.keys()) {
    rememberChild(relative);
  }
  for (const relative of packageRecord.files.keys()) {
    rememberChild(relative);
  }
  return [...entries].sort();
}

async function assertStagedPackageStable(packageRecord, parentObserved) {
  const visit = async (relative, observedByParent) => {
    const directory = stagedPath(packageRecord.root, relative);
    const expected = packageRecord.directories.get(relative);
    const before = await lstat(directory, { bigint: true });
    if (
      expected === undefined ||
      !sameDirectory(expected, before) ||
      !sameDirectory(observedByParent, before) ||
      before.isSymbolicLink() ||
      !before.isDirectory() ||
      !ownedByCurrentUser(before) ||
      (process.platform !== "win32" && Number(before.mode & 0o777n) !== 0o700)
    ) {
      throw stagingError();
    }
    const entries = (await readdir(directory)).sort();
    const expectedEntries = expectedChildEntries(packageRecord, relative);
    if (
      entries.length !== expectedEntries.length ||
      entries.some((entry, index) => entry !== expectedEntries[index])
    ) {
      throw stagingError();
    }
    for (const entry of entries) {
      const childRelative = relative === "" ? entry : `${relative}/${entry}`;
      const filename = path.join(directory, entry);
      const child = await lstat(filename, { bigint: true });
      if (child.isSymbolicLink() || !ownedByCurrentUser(child)) {
        throw stagingError();
      }
      const expectedDirectory = packageRecord.directories.get(childRelative);
      if (expectedDirectory !== undefined) {
        if (!sameDirectory(expectedDirectory, child) || !child.isDirectory()) {
          throw stagingError();
        }
        await visit(childRelative, child);
        continue;
      }
      const expectedFile = packageRecord.files.get(childRelative);
      if (
        expectedFile === undefined ||
        !sameFile(expectedFile.metadata, child) ||
        child.nlink !== 1n ||
        (process.platform !== "win32" &&
          Number(child.mode & 0o777n) !== expectedFile.mode)
      ) {
        throw stagingError();
      }
      const content = await validatedRead(filename);
      if (
        !sameFile(child, content.metadata) ||
        !sameFile(expectedFile.metadata, content.metadata) ||
        !content.content.equals(expectedFile.content)
      ) {
        throw stagingError();
      }
    }
    const after = await lstat(directory, { bigint: true });
    if (
      !sameDirectory(before, after) ||
      !sameDirectory(expected, after) ||
      after.isSymbolicLink() ||
      !ownedByCurrentUser(after)
    ) {
      throw stagingError();
    }
  };
  await visit("", parentObserved);
}

async function assertStagedRootStable(
  outputRoot,
  outputIdentity,
  packageRecords,
  { includeMarker = false } = {},
) {
  const before = await assertOwnedRoot(outputRoot, outputIdentity);
  if (
    process.platform !== "win32" &&
    Number(before.mode & 0o777n) !== 0o700
  ) {
    throw stagingError();
  }
  await exactEntries(outputRoot, [
    ...(includeMarker ? [".complete"] : []),
    ...packageRecords.map(({ name }) => name),
  ]);
  for (const packageRecord of packageRecords) {
    const observed = await lstat(packageRecord.root, { bigint: true });
    if (
      !sameDirectory(packageRecord.identity, observed) ||
      observed.isSymbolicLink() ||
      !ownedByCurrentUser(observed)
    ) {
      throw stagingError();
    }
    await assertStagedPackageStable(packageRecord, observed);
  }
  const after = await assertOwnedRoot(outputRoot, outputIdentity);
  if (!sameNode(before, after)) {
    throw stagingError();
  }
  await exactEntries(outputRoot, [
    ...(includeMarker ? [".complete"] : []),
    ...packageRecords.map(({ name }) => name),
  ]);
}

export function stagedFileOpenFlags(platform = process.platform) {
  if (platform === "win32") {
    return constants.O_RDWR;
  }
  return constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0);
}

async function syncStagedFile(filename, expected) {
  const before = await lstat(filename, { bigint: true });
  if (
    !sameFile(expected.metadata, before) ||
    before.isSymbolicLink() ||
    before.nlink !== 1n ||
    !ownedByCurrentUser(before)
  ) {
    throw stagingError();
  }
  const handle = await open(filename, stagedFileOpenFlags());
  try {
    const opened = await handle.stat({ bigint: true });
    if (!sameFile(before, opened)) {
      throw stagingError();
    }
    await handle.sync();
    const openedAfter = await handle.stat({ bigint: true });
    const pathAfter = await lstat(filename, { bigint: true });
    if (
      !sameFile(opened, openedAfter) ||
      !sameFile(opened, pathAfter) ||
      pathAfter.isSymbolicLink() ||
      !ownedByCurrentUser(pathAfter)
    ) {
      throw stagingError();
    }
  } finally {
    await handle.close();
  }
}

async function syncStagedPackages(packageRecords) {
  for (const packageRecord of packageRecords) {
    for (const [relative, expected] of [...packageRecord.files].sort(
      ([left], [right]) => (left < right ? -1 : left > right ? 1 : 0),
    )) {
      await syncStagedFile(stagedPath(packageRecord.root, relative), expected);
    }
  }
  for (const packageRecord of packageRecords) {
    const directories = [...packageRecord.directories].sort(
      ([left], [right]) => {
        const depth = (value) => (value === "" ? 0 : value.split("/").length);
        return depth(right) - depth(left) ||
          (left < right ? -1 : left > right ? 1 : 0);
      },
    );
    for (const [relative, expected] of directories) {
      await syncDirectory(stagedPath(packageRecord.root, relative), expected);
    }
  }
}

function acquireInvalidCompletionMarker(outputRoot) {
  const marker = path.join(outputRoot, ".complete");
  const fd = openSync(
    marker,
    constants.O_CREAT | constants.O_EXCL | constants.O_WRONLY,
    0o600,
  );
  try {
    const opened = fstatSync(fd, { bigint: true });
    if (
      opened.isSymbolicLink() ||
      !opened.isFile() ||
      opened.nlink !== 1n ||
      !ownedByCurrentUser(opened)
    ) {
      throw stagingError();
    }
    const acquiredPath = lstatSync(marker, { bigint: true });
    if (
      !sameFile(opened, acquiredPath) ||
      acquiredPath.size !== 0n ||
      acquiredPath.isSymbolicLink() ||
      !ownedByCurrentUser(acquiredPath)
    ) {
      throw stagingError();
    }
    fchmodSync(fd, 0o644);
    fsyncSync(fd);
    const invalid = fstatSync(fd, { bigint: true });
    const invalidPath = lstatSync(marker, { bigint: true });
    if (
      !sameFile(opened, invalid) ||
      !sameFile(invalid, invalidPath) ||
      invalid.size !== 0n ||
      invalid.nlink !== 1n ||
      invalid.isSymbolicLink() ||
      !ownedByCurrentUser(invalid) ||
      (process.platform !== "win32" &&
        Number(invalid.mode & 0o777n) !== 0o644)
    ) {
      throw stagingError();
    }
    return { fd, identity: invalid, path: marker };
  } catch (error) {
    try {
      closeSync(fd);
    } catch {
      // Preserve the fixed staging failure if closing an invalid marker fails.
    }
    throw error;
  }
}

async function assertInvalidCompletionMarker(markerRecord) {
  const opened = fstatSync(markerRecord.fd, { bigint: true });
  const current = await lstat(markerRecord.path, { bigint: true });
  if (
    !sameFile(markerRecord.identity, opened) ||
    !sameFile(opened, current) ||
    current.size !== 0n ||
    current.isSymbolicLink() ||
    !ownedByCurrentUser(current) ||
    (process.platform !== "win32" &&
      Number(current.mode & 0o777n) !== 0o644)
  ) {
    throw stagingError();
  }
}

function closeInvalidCompletionMarker(markerRecord) {
  if (markerRecord?.fd === undefined) {
    return;
  }
  try {
    closeSync(markerRecord.fd);
  } catch {
    // A close failure must not replace the fixed staging failure.
  }
  markerRecord.fd = undefined;
}

function commitCompletionMarker(markerRecord) {
  let offset = 0;
  try {
    const opened = fstatSync(markerRecord.fd, { bigint: true });
    const current = lstatSync(markerRecord.path, { bigint: true });
    if (
      !sameFile(markerRecord.identity, opened) ||
      !sameFile(opened, current) ||
      current.size !== 0n ||
      current.isSymbolicLink() ||
      !ownedByCurrentUser(current) ||
      (process.platform !== "win32" &&
        Number(current.mode & 0o777n) !== 0o644)
    ) {
      throw stagingError();
    }
    while (offset < COMPLETION_MARKER_CONTENT.length) {
      const written = writeSync(
        markerRecord.fd,
        COMPLETION_MARKER_CONTENT,
        offset,
        COMPLETION_MARKER_CONTENT.length - offset,
        offset,
      );
      if (written <= 0) {
        throw stagingError();
      }
      offset += written;
    }
  } catch (error) {
    closeInvalidCompletionMarker(markerRecord);
    throw error;
  }
  closeInvalidCompletionMarker(markerRecord);
}

export async function stagePackages(options) {
  let pendingCompletionMarker;
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
    if (!privateOutputParent(outputParentIdentity)) {
      throw stagingError();
    }
    await assertAbsent(options.outputRoot);
    const sourcePlan = await validateSourceTree(
      options.repositoryRoot,
      options.version,
    );
    const binaryPlan = await validateBinaryTree(options.binaryRoot, targets);
    await assertPlanStable(sourcePlan);
    await assertPlanStable(binaryPlan);

    const ownedOutputIdentity = await createPrivateDirectory(options.outputRoot);
    const acquiredOutputParent = await lstat(outputParent, { bigint: true });
    if (
      (await realpath(options.outputRoot)) !== options.outputRoot ||
      !sameNode(outputParentIdentity, acquiredOutputParent) ||
      !privateOutputParent(acquiredOutputParent)
    ) {
      throw stagingError();
    }

    const publishedPackages = [];
    for (const target of targets) {
      publishedPackages.push(
        await stageNative(
          options.repositoryRoot,
          options.binaryRoot,
          options.outputRoot,
          target,
          sourcePlan,
          binaryPlan,
        ),
      );
    }
    publishedPackages.push(
      await stageLauncher(options.repositoryRoot, options.outputRoot, sourcePlan),
    );

    const [repositoryAfter, binaryAfter, outputParentAfter, completedRoot] =
      await Promise.all([
        lstat(options.repositoryRoot, { bigint: true }),
        lstat(options.binaryRoot, { bigint: true }),
        lstat(outputParent, { bigint: true }),
        lstat(options.outputRoot, { bigint: true }),
      ]);
    if (
      !sameNode(repositoryIdentity, repositoryAfter) ||
      !sameNode(binaryIdentity, binaryAfter) ||
      !sameNode(outputParentIdentity, outputParentAfter) ||
      !privateOutputParent(outputParentAfter) ||
      !sameNode(ownedOutputIdentity, completedRoot) ||
      completedRoot.isSymbolicLink() ||
      !completedRoot.isDirectory()
    ) {
      throw stagingError();
    }
    await assertPlanStable(sourcePlan);
    await assertPlanStable(binaryPlan);
    await exactEntries(
      options.outputRoot,
      publishedPackages.map(({ name }) => name),
    );

    await assertStagedRootStable(
      options.outputRoot,
      ownedOutputIdentity,
      publishedPackages,
    );
    await syncStagedPackages(publishedPackages);
    await syncDirectory(options.outputRoot, completedRoot);
    await assertStagedRootStable(
      options.outputRoot,
      ownedOutputIdentity,
      publishedPackages,
    );
    await assertPlanStable(sourcePlan);
    await assertPlanStable(binaryPlan);
    pendingCompletionMarker = acquireInvalidCompletionMarker(
      options.outputRoot,
    );
    await assertStagedRootStable(
      options.outputRoot,
      ownedOutputIdentity,
      publishedPackages,
      { includeMarker: true },
    );
    await assertInvalidCompletionMarker(pendingCompletionMarker);
    await assertPlanStable(sourcePlan);
    await assertPlanStable(binaryPlan);
    await syncDirectory(options.outputRoot, ownedOutputIdentity);
    await syncDirectory(outputParent, outputParentIdentity);
    await assertStagedRootStable(
      options.outputRoot,
      ownedOutputIdentity,
      publishedPackages,
      { includeMarker: true },
    );
    await assertInvalidCompletionMarker(pendingCompletionMarker);
    const [finalRoot, finalParent] = await Promise.all([
      lstat(options.outputRoot, { bigint: true }),
      lstat(outputParent, { bigint: true }),
    ]);
    if (
      !sameNode(ownedOutputIdentity, finalRoot) ||
      finalRoot.isSymbolicLink() ||
      !finalRoot.isDirectory() ||
      !sameNode(outputParentIdentity, finalParent) ||
      !privateOutputParent(finalParent)
    ) {
      throw stagingError();
    }
    await assertPlanStable(sourcePlan);
    await assertPlanStable(binaryPlan);

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
    commitCompletionMarker(pendingCompletionMarker);
    return staged;
  } catch {
    closeInvalidCompletionMarker(pendingCompletionMarker);
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
