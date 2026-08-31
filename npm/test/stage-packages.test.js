import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
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
  stat,
  symlink,
  writeFile,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { after, test } from "node:test";
import { fileURLToPath } from "node:url";

import { PACKAGE_VERSION, TARGETS } from "../scripts/package-config.js";
import { stagePackages } from "../scripts/stage-packages.js";
import { packAndVerify, sourceCheck } from "../scripts/verify-packages.js";

const npmRoot = path.dirname(fileURLToPath(new URL("../package.json", import.meta.url)));
const sourceRepositoryRoot = path.dirname(npmRoot);
const temporaryRoots = [];

after(async () => {
  await Promise.all(
    temporaryRoots.splice(0).map((root) => rm(root, { force: true, recursive: true })),
  );
});

async function pathExists(filename) {
  try {
    await lstat(filename);
    return true;
  } catch (error) {
    if (error?.code === "ENOENT") {
      return false;
    }
    throw error;
  }
}

async function copySourceFile(repositoryRoot, relative) {
  const destination = path.join(repositoryRoot, relative);
  await mkdir(path.dirname(destination), { recursive: true, mode: 0o700 });
  await copyFile(path.join(sourceRepositoryRoot, relative), destination);
}

async function stagingFixture() {
  const lexicalRoot = await mkdtemp(path.join(os.tmpdir(), "ai-cli-gateway-stage-test-"));
  const fixtureRoot = await realpath(lexicalRoot);
  temporaryRoots.push(fixtureRoot);

  const repositoryRoot = path.join(fixtureRoot, "repository");
  const binaryRoot = path.join(fixtureRoot, "binaries");
  const outputParent = path.join(fixtureRoot, "output");
  const outputRoot = path.join(outputParent, "npm-staging");
  await Promise.all([
    mkdir(repositoryRoot, { mode: 0o700 }),
    mkdir(binaryRoot, { mode: 0o700 }),
    mkdir(outputParent, { mode: 0o700 }),
  ]);

  const sourceFiles = [
    "LICENSE",
    "npm/launcher/package.json",
    "npm/launcher/README.md",
    "npm/launcher/bin/ai-cli-gateway.js",
    "npm/launcher/lib/launcher.js",
    ...TARGETS.flatMap((target) => [
      `npm/platforms/${target.key}/package.json`,
      `npm/platforms/${target.key}/README.md`,
    ]),
  ];
  await Promise.all(sourceFiles.map((relative) => copySourceFile(repositoryRoot, relative)));
  await chmod(path.join(repositoryRoot, "npm/launcher/bin/ai-cli-gateway.js"), 0o755);

  for (const target of TARGETS) {
    const directory = path.join(binaryRoot, target.stagingDirectory);
    await mkdir(directory, { mode: 0o700 });
    await writeFile(path.join(directory, target.executable), `${target.key}\n`, {
      mode: target.platform === "win32" ? 0o644 : 0o755,
    });
  }

  return { binaryRoot, fixtureRoot, outputParent, outputRoot, repositoryRoot };
}

function stagingOptions(fixture, overrides = {}) {
  return {
    repositoryRoot: fixture.repositoryRoot,
    binaryRoot: fixture.binaryRoot,
    outputRoot: fixture.outputRoot,
    version: PACKAGE_VERSION,
    ...overrides,
  };
}

async function assertStagingFailure(options) {
  await assert.rejects(stagePackages(options), {
    name: "Error",
    message: "npm package staging failed",
  });
}

async function assertNoOwnedTemporaryRoot(fixture) {
  const entries = await readdir(fixture.outputParent);
  assert.equal(
    entries.some((entry) => entry.startsWith(`.${path.basename(fixture.outputRoot)}-`)),
    false,
  );
}

async function packageTree(root) {
  const files = [];
  async function visit(directory, prefix = "") {
    const entries = await readdir(directory, { withFileTypes: true });
    for (const entry of entries.sort((left, right) =>
      left.name < right.name ? -1 : left.name > right.name ? 1 : 0)) {
      const relative = prefix === "" ? entry.name : `${prefix}/${entry.name}`;
      if (entry.isDirectory()) {
        await visit(path.join(directory, entry.name), relative);
      } else {
        files.push(relative);
      }
    }
  }
  await visit(root);
  return files;
}

async function mode(filename) {
  return (await stat(filename)).mode & 0o777;
}

test("stages all native packages followed by the launcher", async () => {
  const fixture = await stagingFixture();
  const staged = await stagePackages(stagingOptions(fixture));

  assert.deepEqual(
    staged.map(({ name }) => name),
    [
      "ai-cli-gateway-darwin-x64",
      "ai-cli-gateway-darwin-arm64",
      "ai-cli-gateway-linux-x64",
      "ai-cli-gateway-linux-arm64",
      "ai-cli-gateway-win32-x64",
      "ai-cli-gateway",
    ],
  );
  assert.deepEqual(staged, [
    ...TARGETS.map((target) => ({
      name: target.packageName,
      version: PACKAGE_VERSION,
      root: path.join(fixture.outputRoot, target.key),
    })),
    {
      name: "ai-cli-gateway",
      version: PACKAGE_VERSION,
      root: path.join(fixture.outputRoot, "launcher"),
    },
  ]);
  assert.equal(await mode(fixture.outputRoot), 0o700);

  for (const target of TARGETS) {
    const packageRoot = path.join(fixture.outputRoot, target.key);
    const executable = path.join(packageRoot, "bin", target.executable);
    assert.deepEqual(await packageTree(packageRoot), [
      "LICENSE",
      "README.md",
      `bin/${target.executable}`,
      "package.json",
    ]);
    assert.equal(await mode(path.join(packageRoot, "LICENSE")), 0o644);
    assert.equal(await mode(path.join(packageRoot, "README.md")), 0o644);
    assert.equal(await mode(path.join(packageRoot, "package.json")), 0o644);
    assert.equal(await mode(executable), target.platform === "win32" ? 0o644 : 0o755);
    assert.equal(await readFile(executable, "utf8"), `${target.key}\n`);
  }

  const launcherRoot = path.join(fixture.outputRoot, "launcher");
  assert.deepEqual(await packageTree(launcherRoot), [
    "LICENSE",
    "README.md",
    "bin/ai-cli-gateway.js",
    "lib/launcher.js",
    "package.json",
  ]);
  assert.equal(await mode(path.join(launcherRoot, "LICENSE")), 0o644);
  assert.equal(await mode(path.join(launcherRoot, "README.md")), 0o644);
  assert.equal(await mode(path.join(launcherRoot, "package.json")), 0o644);
  assert.equal(await mode(path.join(launcherRoot, "lib/launcher.js")), 0o644);
  assert.equal(await mode(path.join(launcherRoot, "bin/ai-cli-gateway.js")), 0o755);
});

test("stages one exact target and the launcher", async () => {
  const fixture = await stagingFixture();
  const target = TARGETS[2];
  const staged = await stagePackages(stagingOptions(fixture, { targets: [target] }));

  assert.deepEqual(staged, [
    {
      name: target.packageName,
      version: PACKAGE_VERSION,
      root: path.join(fixture.outputRoot, target.key),
    },
    {
      name: "ai-cli-gateway",
      version: PACKAGE_VERSION,
      root: path.join(fixture.outputRoot, "launcher"),
    },
  ]);
  assert.deepEqual((await readdir(fixture.outputRoot)).sort(), ["launcher", target.key]);
});

test("rejects every relative root", async (t) => {
  for (const field of ["repositoryRoot", "binaryRoot", "outputRoot"]) {
    await t.test(field, async () => {
      const fixture = await stagingFixture();
      await assertStagingFailure(
        stagingOptions(fixture, { [field]: path.relative(process.cwd(), fixture[field]) }),
      );
      assert.equal(await pathExists(fixture.outputRoot), false);
      await assertNoOwnedTemporaryRoot(fixture);
    });
  }
});

test("rejects linked repository, binary, and output-parent roots", async (t) => {
  for (const field of ["repositoryRoot", "binaryRoot"]) {
    await t.test(field, async () => {
      const fixture = await stagingFixture();
      const linked = path.join(fixture.fixtureRoot, `linked-${field}`);
      await symlink(fixture[field], linked, process.platform === "win32" ? "junction" : "dir");
      await assertStagingFailure(stagingOptions(fixture, { [field]: linked }));
      assert.equal(await pathExists(fixture.outputRoot), false);
      await assertNoOwnedTemporaryRoot(fixture);
    });
  }

  await t.test("outputRoot parent", async () => {
    const fixture = await stagingFixture();
    const linkedParent = path.join(fixture.fixtureRoot, "linked-output");
    await symlink(
      fixture.outputParent,
      linkedParent,
      process.platform === "win32" ? "junction" : "dir",
    );
    await assertStagingFailure(
      stagingOptions(fixture, { outputRoot: path.join(linkedParent, "npm-staging") }),
    );
    assert.equal(await pathExists(fixture.outputRoot), false);
    await assertNoOwnedTemporaryRoot(fixture);
  });
});

test("rejects linked intermediate source and binary directories", async (t) => {
  await t.test("repository npm directory", async () => {
    const fixture = await stagingFixture();
    const npmDirectory = path.join(fixture.repositoryRoot, "npm");
    const realNpmDirectory = path.join(fixture.repositoryRoot, "npm.real");
    await rename(npmDirectory, realNpmDirectory);
    await symlink(
      path.basename(realNpmDirectory),
      npmDirectory,
      process.platform === "win32" ? "junction" : "dir",
    );

    await assertStagingFailure(stagingOptions(fixture));
    assert.equal(await pathExists(fixture.outputRoot), false);
    await assertNoOwnedTemporaryRoot(fixture);
  });

  await t.test("binary staging directory", async () => {
    const fixture = await stagingFixture();
    const target = TARGETS[0];
    const directory = path.join(fixture.binaryRoot, target.stagingDirectory);
    const realDirectory = `${directory}.real`;
    await rename(directory, realDirectory);
    await symlink(
      path.basename(realDirectory),
      directory,
      process.platform === "win32" ? "junction" : "dir",
    );

    await assertStagingFailure(stagingOptions(fixture));
    assert.equal(await pathExists(fixture.outputRoot), false);
    await assertNoOwnedTemporaryRoot(fixture);
  });
});

test("rejects a pre-existing output root without changing it", async () => {
  const fixture = await stagingFixture();
  const marker = path.join(fixture.outputRoot, "owned-by-caller");
  await mkdir(fixture.outputRoot, { mode: 0o700 });
  await writeFile(marker, "keep\n", "utf8");

  await assertStagingFailure(stagingOptions(fixture));

  assert.equal(await readFile(marker, "utf8"), "keep\n");
  await assertNoOwnedTemporaryRoot(fixture);
});

test(
  "rejects a world-writable non-sticky output parent",
  { skip: process.platform === "win32" },
  async () => {
    const fixture = await stagingFixture();
    await chmod(fixture.outputParent, 0o777);

    await assertStagingFailure(stagingOptions(fixture));

    assert.equal(await pathExists(fixture.outputRoot), false);
    await assertNoOwnedTemporaryRoot(fixture);
  },
);

test("rejects a missing native binary", async () => {
  const fixture = await stagingFixture();
  const target = TARGETS[0];
  await rm(path.join(fixture.binaryRoot, target.stagingDirectory, target.executable));

  await assertStagingFailure(stagingOptions(fixture));

  assert.equal(await pathExists(fixture.outputRoot), false);
  await assertNoOwnedTemporaryRoot(fixture);
});

test("rejects a linked native binary", async () => {
  const fixture = await stagingFixture();
  const target = TARGETS[0];
  const binary = path.join(fixture.binaryRoot, target.stagingDirectory, target.executable);
  const realBinary = `${binary}.real`;
  await rename(binary, realBinary);
  await symlink(path.basename(realBinary), binary, "file");

  await assertStagingFailure(stagingOptions(fixture));

  assert.equal(await pathExists(fixture.outputRoot), false);
  await assertNoOwnedTemporaryRoot(fixture);
});

test("rejects a source package version mismatch", async () => {
  const fixture = await stagingFixture();
  const manifestPath = path.join(fixture.repositoryRoot, "npm/platforms/linux-x64/package.json");
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  manifest.version = "0.2.2";
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, "utf8");

  await assertStagingFailure(stagingOptions(fixture));

  assert.equal(await pathExists(fixture.outputRoot), false);
  await assertNoOwnedTemporaryRoot(fixture);
});

test("rejects unexpected source package files", async (t) => {
  for (const relative of [
    "npm/launcher/unexpected.txt",
    "npm/platforms/linux-x64/bin/ai-cli-gateway",
  ]) {
    await t.test(relative, async () => {
      const fixture = await stagingFixture();
      const unexpected = path.join(fixture.repositoryRoot, relative);
      await mkdir(path.dirname(unexpected), { recursive: true, mode: 0o700 });
      await writeFile(unexpected, "unexpected\n", "utf8");

      await assertStagingFailure(stagingOptions(fixture));

      assert.equal(await pathExists(fixture.outputRoot), false);
      await assertNoOwnedTemporaryRoot(fixture);
    });
  }
});

test("rejects duplicate targets", async () => {
  const fixture = await stagingFixture();

  await assertStagingFailure(
    stagingOptions(fixture, { targets: [TARGETS[0], TARGETS[0]] }),
  );

  assert.equal(await pathExists(fixture.outputRoot), false);
  await assertNoOwnedTemporaryRoot(fixture);
});

function waitForLine(child, expected) {
  return new Promise((resolve, reject) => {
    let stdout = "";
    const timeout = setTimeout(() => reject(new Error(`timed out waiting for ${expected}`)), 10_000);
    child.stdout.setEncoding("utf8");
    child.stdout.on("data", (chunk) => {
      stdout += chunk;
      if (stdout.split(/\r?\n/u).includes(expected)) {
        clearTimeout(timeout);
        resolve();
      }
    });
    child.once("error", (error) => {
      clearTimeout(timeout);
      reject(error);
    });
    child.once("exit", (code, signal) => {
      if (!stdout.split(/\r?\n/u).includes(expected)) {
        clearTimeout(timeout);
        reject(new Error(`replacement process exited early: ${code ?? signal}`));
      }
    });
  });
}

function outputReplacementProcess(outputRoot, capturedRoot) {
  const source = `
    const fs = require("node:fs");
    const path = require("node:path");
    const [outputRoot, capturedRoot] = process.argv.slice(1);
    fs.writeSync(1, "READY\\n");
    const deadline = Date.now() + 10_000;
    while (Date.now() < deadline) {
      try {
        fs.renameSync(outputRoot, capturedRoot);
        fs.mkdirSync(outputRoot, { mode: 0o700 });
        fs.writeFileSync(path.join(outputRoot, "attacker-marker"), "replacement\\n");
        fs.writeSync(1, "REPLACED\\n");
        process.exit(0);
      } catch (error) {
        if (error.code !== "ENOENT") throw error;
      }
    }
    process.exit(2);
  `;
  return spawn(process.execPath, ["--eval", source, outputRoot, capturedRoot], {
    shell: false,
    stdio: ["ignore", "pipe", "pipe"],
  });
}

test("rejects output-root replacement and preserves the replacement", async () => {
  const fixture = await stagingFixture();
  const capturedRoot = path.join(fixture.outputParent, "captured-staging");
  const replacer = outputReplacementProcess(fixture.outputRoot, capturedRoot);
  await waitForLine(replacer, "READY");

  await assertStagingFailure(stagingOptions(fixture));
  await waitForLine(replacer, "REPLACED");

  assert.equal(
    await readFile(path.join(fixture.outputRoot, "attacker-marker"), "utf8"),
    "replacement\n",
  );
  assert.equal(await pathExists(capturedRoot), true);
  await assertNoOwnedTemporaryRoot(fixture);
});

test("stage CLI rejects an unknown option with a fixed path-free error", async () => {
  const script = path.join(npmRoot, "scripts", "stage-packages.js");
  const child = spawn(process.execPath, [script, "--unknown"], {
    shell: false,
    stdio: ["ignore", "pipe", "pipe"],
  });
  let stdout = "";
  let stderr = "";
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");
  child.stdout.on("data", (chunk) => {
    stdout += chunk;
  });
  child.stderr.on("data", (chunk) => {
    stderr += chunk;
  });
  const result = await new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("close", (code, signal) => resolve({ code, signal }));
  });

  assert.deepEqual(result, { code: 1, signal: null });
  assert.equal(stdout, "");
  assert.equal(stderr, "npm package staging failed\n");
  assert.equal(stderr.includes(process.cwd()), false);
});

test("stage CLI accepts the exact target-selecting shape", async () => {
  const fixture = await stagingFixture();
  const target = TARGETS[1];
  const script = path.join(npmRoot, "scripts", "stage-packages.js");
  const result = await runNodeScript(script, [
    "--repository-root",
    fixture.repositoryRoot,
    "--binary-root",
    fixture.binaryRoot,
    "--output-root",
    fixture.outputRoot,
    "--version",
    PACKAGE_VERSION,
    "--target",
    target.key,
  ]);

  assert.deepEqual(result, { code: 0, signal: null, stderr: "", stdout: "" });
  assert.deepEqual(
    (await readdir(fixture.outputRoot)).sort(),
    ["launcher", target.key].sort(),
  );
});

function npmFilename(name, version) {
  return `${name}-${version}.tgz`;
}

function expectedPackageFiles(target) {
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

async function writeFakeNpm(fixtureRoot, mutation = "none") {
  const executable = path.join(fixtureRoot, `fake npm ${mutation}.js`);
  const source = `#!${process.execPath}
const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");

const mutation = ${JSON.stringify(mutation)};
const args = process.argv.slice(2);
if (
  args.length !== 5 ||
  args[0] !== "pack" ||
  args[1] !== "--ignore-scripts" ||
  args[2] !== "--json" ||
  args[3] !== "--pack-destination" ||
  !path.isAbsolute(args[4]) ||
  process.env.AI_CLI_GATEWAY_SECRET_SENTINEL !== undefined ||
  process.env.NODE_AUTH_TOKEN !== undefined ||
  process.env.NPM_TOKEN !== undefined
) {
  process.stderr.write("fake npm received an open invocation\\n");
  process.exit(91);
}

const packageRoot = process.cwd();
const manifest = JSON.parse(fs.readFileSync(path.join(packageRoot, "package.json"), "utf8"));
const files = [];
function visit(directory, prefix = "") {
  const entries = fs.readdirSync(directory, { withFileTypes: true });
  entries.sort((left, right) => left.name < right.name ? -1 : left.name > right.name ? 1 : 0);
  for (const entry of entries) {
    const relative = prefix === "" ? entry.name : prefix + "/" + entry.name;
    const filename = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      visit(filename, relative);
    } else {
      const metadata = fs.lstatSync(filename);
      files.push({ path: relative, size: metadata.size, mode: metadata.mode & 0o777 });
    }
  }
}
visit(packageRoot);

const filename = manifest.name + "-" + manifest.version + ".tgz";
const tarball = Buffer.from("verified fake tarball for " + manifest.name + "\\n");
const tarballPath = path.join(args[4], filename);
if (mutation === "linked-tarball") {
  const outside = path.join(path.dirname(args[4]), "outside-" + filename);
  fs.writeFileSync(outside, tarball);
  fs.symlinkSync(outside, tarballPath, "file");
} else {
  fs.writeFileSync(tarballPath, tarball, { flag: "wx", mode: 0o644 });
}

const record = {
  id: manifest.name + "@" + manifest.version,
  name: manifest.name,
  version: manifest.version,
  size: tarball.length,
  unpackedSize: files.reduce((sum, file) => sum + file.size, 0),
  shasum: crypto.createHash("sha1").update(tarball).digest("hex"),
  integrity: "sha512-" + crypto.createHash("sha512").update(tarball).digest("base64"),
  filename,
  files,
  entryCount: files.length,
  bundled: [],
};

if (mutation === "wrong-name") record.name = "wrong-package";
if (mutation === "wrong-version") record.version = "9.9.9";
if (mutation === "wrong-filename") record.filename = "wrong.tgz";
if (mutation === "wrong-integrity") record.integrity = "sha512-d3Jvbmc=";
if (mutation === "wrong-shasum") record.shasum = "0000000000000000000000000000000000000000";
if (mutation === "wrong-size") record.size += 1;
if (mutation === "wrong-path") record.files[0].path = "../escape";
if (mutation === "wrong-mode") record.files[0].mode = 0o777;
if (mutation === "wrong-file-size") record.files[0].size += 1;
if (mutation === "wrong-file-count") record.files.pop();
if (mutation === "wrong-entry-count") record.entryCount += 1;
if (mutation === "duplicate-path") record.files[1].path = record.files[0].path;
if (mutation === "bundled-dependency") record.bundled = ["malicious"];

if (mutation === "invalid-json") {
  process.stdout.write("not json\\n");
} else if (mutation === "extra-record") {
  process.stdout.write(JSON.stringify([record, record]) + "\\n");
} else {
  process.stdout.write(JSON.stringify([record]) + "\\n");
}
`;
  await writeFile(executable, source, { mode: 0o755 });
  await chmod(executable, 0o755);
  return executable;
}

async function packFixture({ mutation = "none", targets = [TARGETS[2]] } = {}) {
  const fixture = await stagingFixture();
  await stagePackages(stagingOptions(fixture, { targets }));
  const tarballRoot = path.join(fixture.fixtureRoot, "tarballs");
  await mkdir(tarballRoot, { mode: 0o700 });
  const descriptor = path.join(tarballRoot, "packages.json");
  const npmExecutable = await writeFakeNpm(fixture.fixtureRoot, mutation);
  return {
    ...fixture,
    descriptor,
    npmExecutable,
    tarballRoot,
    targets,
    options: {
      stagingRoot: fixture.outputRoot,
      tarballRoot,
      descriptor,
      version: PACKAGE_VERSION,
      npmExecutable,
    },
  };
}

async function assertVerificationFailure(options) {
  await assert.rejects(packAndVerify(options), {
    name: "Error",
    message: "npm package verification failed",
  });
}

test("packs verified descriptors in native-first canonical order", async () => {
  const fixture = await packFixture({ targets: TARGETS });
  process.env.AI_CLI_GATEWAY_SECRET_SENTINEL = "must-not-reach-child";
  let descriptors;
  try {
    descriptors = await packAndVerify(fixture.options);
  } finally {
    delete process.env.AI_CLI_GATEWAY_SECRET_SENTINEL;
  }

  assert.deepEqual(
    descriptors.map(({ name }) => name),
    [...TARGETS.map(({ packageName }) => packageName), "ai-cli-gateway"],
  );
  for (const [index, descriptor] of descriptors.entries()) {
    const target = TARGETS[index];
    const name = target?.packageName ?? "ai-cli-gateway";
    assert.deepEqual(Object.keys(descriptor), [
      "name",
      "version",
      "filename",
      "integrity",
      "shasum",
      "size",
      "files",
    ]);
    assert.equal(descriptor.name, name);
    assert.equal(descriptor.version, PACKAGE_VERSION);
    assert.equal(descriptor.filename, npmFilename(name, PACKAGE_VERSION));
    assert.match(descriptor.integrity, /^sha512-[A-Za-z0-9+/]+={0,2}$/u);
    assert.match(descriptor.shasum, /^[0-9a-f]{40}$/u);
    assert.ok(Number.isSafeInteger(descriptor.size) && descriptor.size > 0);
    assert.deepEqual(descriptor.files, expectedPackageFiles(target));
  }

  const serialized = await readFile(fixture.descriptor, "utf8");
  assert.equal(serialized, `${JSON.stringify(descriptors, null, 2)}\n`);
  assert.equal(serialized.includes(fixture.fixtureRoot), false);
  assert.deepEqual(
    (await readdir(fixture.tarballRoot)).sort(),
    [...descriptors.map(({ filename }) => filename), "packages.json"].sort(),
  );
});

test("rejects adversarial npm JSON and metadata fixtures", async (t) => {
  const mutations = [
    "invalid-json",
    "extra-record",
    "wrong-name",
    "wrong-version",
    "wrong-filename",
    "wrong-integrity",
    "wrong-shasum",
    "wrong-size",
    "wrong-path",
    "wrong-mode",
    "wrong-file-size",
    "wrong-file-count",
    "wrong-entry-count",
    "duplicate-path",
    "bundled-dependency",
  ];
  for (const mutation of mutations) {
    await t.test(mutation, async () => {
      const fixture = await packFixture({ mutation });
      await assertVerificationFailure(fixture.options);
      assert.equal(await pathExists(fixture.descriptor), false);
    });
  }
});

test(
  "rejects a linked tarball",
  { skip: process.platform === "win32" },
  async () => {
    const fixture = await packFixture({ mutation: "linked-tarball" });
    await assertVerificationFailure(fixture.options);
    assert.equal(await pathExists(fixture.descriptor), false);
  },
);

test("rejects lifecycle scripts in the staged launcher manifest", async () => {
  const fixture = await packFixture();
  const manifestPath = path.join(fixture.outputRoot, "launcher", "package.json");
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  manifest.scripts = { prepack: "exit 99" };
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, "utf8");

  await assertVerificationFailure(fixture.options);
  assert.equal(await pathExists(fixture.descriptor), false);
});

test("rejects dependencies in a staged native manifest", async () => {
  const fixture = await packFixture();
  const target = fixture.targets[0];
  const manifestPath = path.join(fixture.outputRoot, target.key, "package.json");
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  manifest.dependencies = { malicious: "1.0.0" };
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, "utf8");

  await assertVerificationFailure(fixture.options);
  assert.equal(await pathExists(fixture.descriptor), false);
});

test("rejects relative, linked, and out-of-root verifier inputs", async (t) => {
  await t.test("relative npm executable", async () => {
    const fixture = await packFixture();
    await assertVerificationFailure({
      ...fixture.options,
      npmExecutable: path.relative(process.cwd(), fixture.npmExecutable),
    });
  });

  await t.test("linked npm executable", async () => {
    const fixture = await packFixture();
    const linked = path.join(fixture.fixtureRoot, "linked-npm");
    await symlink(fixture.npmExecutable, linked, "file");
    await assertVerificationFailure({ ...fixture.options, npmExecutable: linked });
  });

  await t.test("linked staging root", async () => {
    const fixture = await packFixture();
    const linked = path.join(fixture.fixtureRoot, "linked-staging");
    await symlink(
      fixture.outputRoot,
      linked,
      process.platform === "win32" ? "junction" : "dir",
    );
    await assertVerificationFailure({ ...fixture.options, stagingRoot: linked });
  });

  await t.test("descriptor outside tarball root", async () => {
    const fixture = await packFixture();
    await assertVerificationFailure({
      ...fixture.options,
      descriptor: path.join(fixture.fixtureRoot, "packages.json"),
    });
  });
});

test("source check validates exact package sources and rejects a native binary", async () => {
  const valid = await stagingFixture();
  await sourceCheck({ repositoryRoot: valid.repositoryRoot, version: PACKAGE_VERSION });

  const invalid = await stagingFixture();
  const target = TARGETS[0];
  const trackedBinary = path.join(
    invalid.repositoryRoot,
    "npm",
    "platforms",
    target.key,
    "bin",
    target.executable,
  );
  await mkdir(path.dirname(trackedBinary), { mode: 0o700 });
  await writeFile(trackedBinary, "tracked native binary\n", "utf8");
  await assert.rejects(
    sourceCheck({ repositoryRoot: invalid.repositoryRoot, version: PACKAGE_VERSION }),
    { name: "Error", message: "npm package verification failed" },
  );
});

function hostTarget() {
  const target = TARGETS.find(
    ({ platform, arch }) => platform === process.platform && arch === process.arch,
  );
  assert.ok(target, `test host ${process.platform}-${process.arch} must be supported`);
  return target;
}

async function installedNpmExecutable() {
  if (process.platform === "win32") {
    return undefined;
  }
  return realpath(path.join(path.dirname(process.execPath), "npm"));
}

test(
  "real npm pack is reproducible for the staged host native package and launcher",
  { skip: process.platform === "win32" },
  async () => {
    const target = hostTarget();
    const npmExecutable = await installedNpmExecutable();
    const descriptorDocuments = [];

    for (let index = 0; index < 2; index += 1) {
      const fixture = await stagingFixture();
      await stagePackages(stagingOptions(fixture, { targets: [target] }));
      const tarballRoot = path.join(fixture.fixtureRoot, "real-tarballs");
      const descriptor = path.join(tarballRoot, "packages.json");
      await mkdir(tarballRoot, { mode: 0o700 });

      const descriptors = await packAndVerify({
        stagingRoot: fixture.outputRoot,
        tarballRoot,
        descriptor,
        version: PACKAGE_VERSION,
        npmExecutable,
      });

      assert.deepEqual(descriptors.map(({ name }) => name), [
        target.packageName,
        "ai-cli-gateway",
      ]);
      assert.deepEqual(descriptors.map(({ files }) => files), [
        expectedPackageFiles(target),
        expectedPackageFiles(),
      ]);
      for (const value of descriptors) {
        const bytes = await readFile(path.join(tarballRoot, value.filename));
        assert.equal(createHash("sha1").update(bytes).digest("hex"), value.shasum);
        assert.equal(
          `sha512-${createHash("sha512").update(bytes).digest("base64")}`,
          value.integrity,
        );
      }
      descriptorDocuments.push(await readFile(descriptor, "utf8"));
    }

    assert.equal(descriptorDocuments[0], descriptorDocuments[1]);
    assert.equal(descriptorDocuments[0].includes(os.tmpdir()), false);
  },
);

async function runNodeScript(script, args) {
  const child = spawn(process.execPath, [script, ...args], {
    shell: false,
    stdio: ["ignore", "pipe", "pipe"],
  });
  let stdout = "";
  let stderr = "";
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");
  child.stdout.on("data", (chunk) => {
    stdout += chunk;
  });
  child.stderr.on("data", (chunk) => {
    stderr += chunk;
  });
  const result = await new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("close", (code, signal) => resolve({ code, signal }));
  });
  return { ...result, stderr, stdout };
}

test("verify CLI accepts only source-check or the exact staging shape", async () => {
  const script = path.join(npmRoot, "scripts", "verify-packages.js");
  const sourceResult = await runNodeScript(script, ["--source-check"]);
  assert.deepEqual(sourceResult, { code: 0, signal: null, stderr: "", stdout: "" });

  const fixture = await stagingFixture();
  await stagePackages(stagingOptions(fixture, { targets: [hostTarget()] }));
  const tarballRoot = path.join(fixture.fixtureRoot, "cli-tarballs");
  const descriptor = path.join(tarballRoot, "packages.json");
  await mkdir(tarballRoot, { mode: 0o700 });
  const stagingResult = await runNodeScript(script, [
    "--staging-root",
    fixture.outputRoot,
    "--tarball-root",
    tarballRoot,
    "--descriptor",
    descriptor,
    "--version",
    PACKAGE_VERSION,
  ]);
  assert.deepEqual(stagingResult, {
    code: 0,
    signal: null,
    stderr: "",
    stdout: "",
  });
  assert.equal(await pathExists(descriptor), true);

  const mixedResult = await runNodeScript(script, ["--source-check", "--version", PACKAGE_VERSION]);
  assert.deepEqual(mixedResult, {
    code: 1,
    signal: null,
    stderr: "npm package verification failed\n",
    stdout: "",
  });
  assert.equal(mixedResult.stderr.includes(process.cwd()), false);
});
