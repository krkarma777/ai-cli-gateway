import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
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
  stat,
  symlink,
  writeFile,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { createRequire, syncBuiltinESMExports } from "node:module";
import { after, test } from "node:test";
import { fileURLToPath } from "node:url";

import { PACKAGE_VERSION, TARGETS } from "../scripts/package-config.js";
import {
  stagedFileOpenFlags,
  stagePackages,
} from "../scripts/stage-packages.js";
import { packAndVerify, sourceCheck } from "../scripts/verify-packages.js";

const npmRoot = path.dirname(fileURLToPath(new URL("../package.json", import.meta.url)));
const sourceRepositoryRoot = path.dirname(npmRoot);
const completionMarkerContent = "ai-cli-gateway npm staging complete\n";
const temporaryRoots = [];
const require = createRequire(import.meta.url);
const mutableFs = require("node:fs");
const mutableFsPromises = require("node:fs/promises");

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

async function assertNoValidCompletionMarker(outputRoot) {
  const marker = path.join(outputRoot, ".complete");
  if (!(await pathExists(marker))) {
    return;
  }
  assert.notEqual(await readFile(marker, "utf8"), completionMarkerContent);
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

async function assertMode(filename, expected) {
  if (process.platform !== "win32") {
    assert.equal(await mode(filename), expected);
  }
}

async function withFsPromisePatches(patches, operation) {
  const originals = new Map();
  for (const [name, replacement] of Object.entries(patches)) {
    originals.set(name, mutableFsPromises[name]);
    mutableFsPromises[name] = replacement;
  }
  syncBuiltinESMExports();
  try {
    return await operation();
  } finally {
    for (const [name, original] of originals) {
      mutableFsPromises[name] = original;
    }
    syncBuiltinESMExports();
  }
}

async function withFsPatches(patches, operation) {
  const originals = new Map();
  for (const [name, replacement] of Object.entries(patches)) {
    originals.set(name, mutableFs[name]);
    mutableFs[name] = replacement;
  }
  syncBuiltinESMExports();
  try {
    return await operation();
  } finally {
    for (const [name, original] of originals) {
      mutableFs[name] = original;
    }
    syncBuiltinESMExports();
  }
}

test("uses a writable staged-file handle only on Windows", () => {
  assert.equal(stagedFileOpenFlags("win32"), constants.O_RDWR);
  assert.equal(
    stagedFileOpenFlags("linux"),
    constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0),
  );
  assert.equal(
    stagedFileOpenFlags("darwin"),
    constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0),
  );
});

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
  await assertMode(fixture.outputRoot, 0o700);
  assert.equal(
    await readFile(path.join(fixture.outputRoot, ".complete"), "utf8"),
    completionMarkerContent,
  );

  for (const target of TARGETS) {
    const packageRoot = path.join(fixture.outputRoot, target.key);
    const executable = path.join(packageRoot, "bin", target.executable);
    assert.deepEqual(await packageTree(packageRoot), [
      "LICENSE",
      "README.md",
      `bin/${target.executable}`,
      "package.json",
    ]);
    await assertMode(path.join(packageRoot, "LICENSE"), 0o644);
    await assertMode(path.join(packageRoot, "README.md"), 0o644);
    await assertMode(path.join(packageRoot, "package.json"), 0o644);
    await assertMode(executable, target.platform === "win32" ? 0o644 : 0o755);
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
  await assertMode(path.join(launcherRoot, "LICENSE"), 0o644);
  await assertMode(path.join(launcherRoot, "README.md"), 0o644);
  await assertMode(path.join(launcherRoot, "package.json"), 0o644);
  await assertMode(path.join(launcherRoot, "lib/launcher.js"), 0o644);
  await assertMode(path.join(launcherRoot, "bin/ai-cli-gateway.js"), 0o755);
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
  assert.deepEqual(
    (await readdir(fixture.outputRoot)).sort(),
    [".complete", "launcher", target.key].sort(),
  );
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

test(
  "rejects group-writable and sticky world-writable output parents",
  { skip: process.platform === "win32" },
  async (t) => {
    for (const [label, unsafeMode] of [
      ["group-writable", 0o770],
      ["sticky world-writable", 0o1777],
    ]) {
      await t.test(label, async () => {
        const fixture = await stagingFixture();
        await chmod(fixture.outputParent, unsafeMode);

        await assertStagingFailure(stagingOptions(fixture));

        assert.equal(await pathExists(fixture.outputRoot), false);
        await assertNoOwnedTemporaryRoot(fixture);
      });
    }
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
  const replaced = waitForLine(replacer, "REPLACED");
  await waitForLine(replacer, "READY");

  await assertStagingFailure(stagingOptions(fixture));
  await replaced;

  assert.equal(
    await readFile(path.join(fixture.outputRoot, "attacker-marker"), "utf8"),
    "replacement\n",
  );
  assert.equal(await pathExists(capturedRoot), true);
  assert.deepEqual(await readdir(fixture.outputRoot), ["attacker-marker"]);
  await assertNoOwnedTemporaryRoot(fixture);
});

test("never clobbers an output root raced after validation", async () => {
  const fixture = await stagingFixture();
  const originalMkdir = mutableFsPromises.mkdir;
  const originalRename = mutableFsPromises.rename;
  const originalLstat = mutableFsPromises.lstat;
  let racedIdentity;
  const reserveRacedRoot = async () => {
    await originalMkdir(fixture.outputRoot, { mode: 0o700 });
    racedIdentity = await originalLstat(fixture.outputRoot, { bigint: true });
  };

  await withFsPromisePatches(
    {
      mkdir: async (directory, options) => {
        if (directory === fixture.outputRoot && racedIdentity === undefined) {
          await reserveRacedRoot();
        }
        return originalMkdir(directory, options);
      },
      rename: async (source, destination) => {
        if (destination === fixture.outputRoot && racedIdentity === undefined) {
          await reserveRacedRoot();
        }
        return originalRename(source, destination);
      },
    },
    () => assertStagingFailure(stagingOptions(fixture)),
  );

  assert.ok(racedIdentity);
  const actual = await lstat(fixture.outputRoot, { bigint: true });
  assert.equal(actual.dev, racedIdentity.dev);
  assert.equal(actual.ino, racedIdentity.ino);
  assert.deepEqual(await readdir(fixture.outputRoot), []);
  await assertNoOwnedTemporaryRoot(fixture);
});

test("never recursively deletes an output-root replacement raced during cleanup", async () => {
  const fixture = await stagingFixture();
  const capturedRoot = path.join(fixture.outputParent, "captured-cleanup-output");
  const replacementMarker = path.join(fixture.outputRoot, "foreign-owner-marker");
  const originalOpenSync = mutableFs.openSync;
  const originalMkdir = mutableFsPromises.mkdir;
  const originalOpen = mutableFsPromises.open;
  const originalRename = mutableFsPromises.rename;
  const originalRm = mutableFsPromises.rm;
  const originalWriteFile = mutableFsPromises.writeFile;
  let cleanupRaced = false;

  await withFsPatches(
    {
      openSync: (filename, ...argumentsAfterFilename) => {
        if (filename === path.join(fixture.outputRoot, ".complete")) {
          throw new Error("injected completion-marker failure");
        }
        return originalOpenSync(filename, ...argumentsAfterFilename);
      },
    },
    () =>
      withFsPromisePatches(
        {
          open: async (filename, ...argumentsAfterFilename) => {
            if (filename === path.join(fixture.outputRoot, ".complete")) {
              throw new Error("injected completion-marker failure");
            }
            return originalOpen(filename, ...argumentsAfterFilename);
          },
          rm: async (directory, options) => {
            if (directory === fixture.outputRoot && !cleanupRaced) {
              await originalRename(fixture.outputRoot, capturedRoot);
              await originalMkdir(fixture.outputRoot, { mode: 0o700 });
              await originalWriteFile(replacementMarker, "foreign owner\n", {
                flag: "wx",
                mode: 0o600,
              });
              cleanupRaced = true;
            }
            return originalRm(directory, options);
          },
        },
        () => assertStagingFailure(stagingOptions(fixture)),
      ),
  );

  if (cleanupRaced) {
    assert.equal(await pathExists(capturedRoot), true);
    assert.equal(await readFile(replacementMarker, "utf8"), "foreign owner\n");
  } else {
    assert.equal(await pathExists(fixture.outputRoot), true);
    assert.equal(await pathExists(path.join(fixture.outputRoot, ".complete")), false);
  }
});

test("never recursively deletes a temporary-root replacement raced during cleanup", async () => {
  const fixture = await stagingFixture();
  const capturedRoot = path.join(fixture.outputParent, "captured-cleanup-temporary");
  const temporaryPrefix = `.${path.basename(fixture.outputRoot)}-`;
  const originalMkdir = mutableFsPromises.mkdir;
  const originalRename = mutableFsPromises.rename;
  const originalRm = mutableFsPromises.rm;
  const originalWriteFile = mutableFsPromises.writeFile;
  let cleanupRaced = false;
  let replacementMarker;

  await withFsPromisePatches(
    {
      mkdir: async (directory, options) => {
        if (directory === fixture.outputRoot) {
          throw new Error("injected output-root acquisition failure");
        }
        return originalMkdir(directory, options);
      },
      rm: async (directory, options) => {
        if (
          !cleanupRaced &&
          path.dirname(directory) === fixture.outputParent &&
          path.basename(directory).startsWith(temporaryPrefix)
        ) {
          await originalRename(directory, capturedRoot);
          await originalMkdir(directory, { mode: 0o700 });
          replacementMarker = path.join(directory, "foreign-owner-marker");
          await originalWriteFile(replacementMarker, "foreign owner\n", {
            flag: "wx",
            mode: 0o600,
          });
          cleanupRaced = true;
        }
        return originalRm(directory, options);
      },
    },
    () => assertStagingFailure(stagingOptions(fixture)),
  );

  if (cleanupRaced) {
    assert.equal(await pathExists(capturedRoot), true);
    assert.equal(await readFile(replacementMarker, "utf8"), "foreign owner\n");
  } else {
    assert.equal(await pathExists(fixture.outputRoot), false);
    await assertNoOwnedTemporaryRoot(fixture);
  }
});

test("rejects child removal immediately before completion-marker acquisition", async () => {
  const fixture = await stagingFixture();
  const target = TARGETS[2];
  const stagedBinary = path.join(
    fixture.outputRoot,
    target.key,
    "bin",
    target.executable,
  );
  const originalOpenSync = mutableFs.openSync;
  const originalRmSync = mutableFs.rmSync;
  const originalOpen = mutableFsPromises.open;
  const originalRm = mutableFsPromises.rm;
  let removed = false;

  await withFsPatches(
    {
      openSync: (filename, ...argumentsAfterFilename) => {
        if (filename === path.join(fixture.outputRoot, ".complete") && !removed) {
          originalRmSync(stagedBinary);
          removed = true;
        }
        return originalOpenSync(filename, ...argumentsAfterFilename);
      },
    },
    () =>
      withFsPromisePatches(
        {
          open: async (filename, ...argumentsAfterFilename) => {
            if (filename === path.join(fixture.outputRoot, ".complete") && !removed) {
              await originalRm(stagedBinary);
              removed = true;
            }
            return originalOpen(filename, ...argumentsAfterFilename);
          },
        },
        () =>
          assertStagingFailure(
            stagingOptions(fixture, { targets: [target] }),
          ),
      ),
  );

  assert.equal(removed, true);
  await assertNoValidCompletionMarker(fixture.outputRoot);
});

test("rejects same-size child mutation immediately after completion-marker acquisition", async () => {
  const fixture = await stagingFixture();
  const target = TARGETS[2];
  const stagedBinary = path.join(
    fixture.outputRoot,
    target.key,
    "bin",
    target.executable,
  );
  const originalOpenSync = mutableFs.openSync;
  const originalWriteFileSync = mutableFs.writeFileSync;
  const originalOpen = mutableFsPromises.open;
  const originalWriteFile = mutableFsPromises.writeFile;
  const expected = Buffer.from(`${target.key}\n`, "utf8");
  const replacement = Buffer.alloc(expected.length, 0x78);
  assert.equal(replacement.equals(expected), false);
  let mutated = false;

  await withFsPatches(
    {
      openSync: (filename, ...argumentsAfterFilename) => {
        const descriptor = originalOpenSync(filename, ...argumentsAfterFilename);
        if (filename === path.join(fixture.outputRoot, ".complete") && !mutated) {
          originalWriteFileSync(stagedBinary, replacement);
          mutated = true;
        }
        return descriptor;
      },
    },
    () =>
      withFsPromisePatches(
        {
          open: async (filename, ...argumentsAfterFilename) => {
            const handle = await originalOpen(filename, ...argumentsAfterFilename);
            if (filename === path.join(fixture.outputRoot, ".complete") && !mutated) {
              await originalWriteFile(stagedBinary, replacement);
              mutated = true;
            }
            return handle;
          },
        },
        () =>
          assertStagingFailure(
            stagingOptions(fixture, { targets: [target] }),
          ),
      ),
  );

  assert.equal(mutated, true);
  await assertNoValidCompletionMarker(fixture.outputRoot);
});

test("keeps the marker invalid when the second output-root sync fails", async () => {
  const fixture = await stagingFixture();
  const target = TARGETS[2];
  const originalOpen = mutableFsPromises.open;
  let outputRootOpens = 0;
  let syncFailed = false;

  await withFsPromisePatches(
    {
      open: async (filename, ...argumentsAfterFilename) => {
        const handle = await originalOpen(filename, ...argumentsAfterFilename);
        if (filename === fixture.outputRoot) {
          outputRootOpens += 1;
          if (outputRootOpens === 2) {
            Object.defineProperty(handle, "sync", {
              configurable: true,
              value: async () => {
                syncFailed = true;
                const error = new Error("injected second output-root sync failure");
                error.code = "EIO";
                throw error;
              },
            });
          }
        }
        return handle;
      },
    },
    () =>
      assertStagingFailure(
        stagingOptions(fixture, { targets: [target] }),
      ),
  );

  assert.equal(syncFailed, true);
  await assertNoValidCompletionMarker(fixture.outputRoot);
});

test("keeps the marker invalid when the output-parent sync fails", async () => {
  const fixture = await stagingFixture();
  const target = TARGETS[2];
  const originalOpen = mutableFsPromises.open;
  let syncFailed = false;

  await withFsPromisePatches(
    {
      open: async (filename, ...argumentsAfterFilename) => {
        const handle = await originalOpen(filename, ...argumentsAfterFilename);
        if (filename === fixture.outputParent) {
          Object.defineProperty(handle, "sync", {
            configurable: true,
            value: async () => {
              syncFailed = true;
              const error = new Error("injected output-parent sync failure");
              error.code = "EIO";
              throw error;
            },
          });
        }
        return handle;
      },
    },
    () =>
      assertStagingFailure(
        stagingOptions(fixture, { targets: [target] }),
      ),
  );

  assert.equal(syncFailed, true);
  await assertNoValidCompletionMarker(fixture.outputRoot);
});

test("publishes the exact completion marker only on successful staging", async () => {
  const fixture = await stagingFixture();
  const target = TARGETS[2];

  await stagePackages(stagingOptions(fixture, { targets: [target] }));

  const marker = path.join(fixture.outputRoot, ".complete");
  assert.equal(await readFile(marker, "utf8"), completionMarkerContent);
  await assertMode(marker, 0o644);
});

test("keeps a short marker write invalid when the next write fails", async () => {
  const fixture = await stagingFixture();
  const target = TARGETS[2];
  const marker = path.join(fixture.outputRoot, ".complete");
  const expected = Buffer.from(completionMarkerContent, "utf8");
  const originalWriteSync = mutableFs.writeSync;
  let markerWrites = 0;

  await withFsPatches(
    {
      writeSync: (fd, buffer, offset, length, position) => {
        if (Buffer.isBuffer(buffer) && buffer.equals(expected)) {
          markerWrites += 1;
          if (markerWrites === 1) {
            const prefixLength = Math.max(1, Math.floor(length / 2));
            return originalWriteSync(
              fd,
              buffer,
              offset,
              prefixLength,
              position,
            );
          }
          const error = new Error("injected completion-marker write failure");
          error.code = "EIO";
          throw error;
        }
        return originalWriteSync(fd, buffer, offset, length, position);
      },
    },
    () =>
      assertStagingFailure(
        stagingOptions(fixture, { targets: [target] }),
      ),
  );

  assert.equal(markerWrites, 2);
  const partial = await readFile(marker);
  assert.ok(partial.length > 0 && partial.length < expected.length);
  assert.equal(partial.equals(expected), false);
  await assertNoValidCompletionMarker(fixture.outputRoot);
});

test("accepts publication when marker close succeeds before reporting an error", async () => {
  const fixture = await stagingFixture();
  const target = TARGETS[2];
  const marker = path.join(fixture.outputRoot, ".complete");
  const originalCloseSync = mutableFs.closeSync;
  const originalFstatSync = mutableFs.fstatSync;
  const warnings = [];
  const captureWarning = (warning) => warnings.push(warning);
  let closedFd;
  let staged;

  process.on("warning", captureWarning);
  try {
    staged = await withFsPatches(
      {
        closeSync: (fd) => {
          originalCloseSync(fd);
          closedFd = fd;
          const error = new Error("injected post-close marker error");
          error.code = "EIO";
          throw error;
        },
      },
      () => stagePackages(stagingOptions(fixture, { targets: [target] })),
    );
    await new Promise((resolve) => setImmediate(resolve));
  } finally {
    process.off("warning", captureWarning);
  }

  assert.deepEqual(staged.map(({ name }) => name), [
    target.packageName,
    "ai-cli-gateway",
  ]);
  assert.equal(await readFile(marker, "utf8"), completionMarkerContent);
  assert.throws(
    () => originalFstatSync(closedFd),
    (error) => error?.code === "EBADF",
  );
  assert.deepEqual(warnings, []);
});

test("fails closed when syncing a copied file fails before marker publication", async () => {
  const fixture = await stagingFixture();
  const target = TARGETS[2];
  const stagedBinary = path.join(
    fixture.outputRoot,
    target.key,
    "bin",
    target.executable,
  );
  const originalOpen = mutableFsPromises.open;
  let syncAttempted = false;

  await withFsPromisePatches(
    {
      open: async (filename, ...argumentsAfterFilename) => {
        const handle = await originalOpen(filename, ...argumentsAfterFilename);
        if (filename === stagedBinary) {
          Object.defineProperty(handle, "sync", {
            configurable: true,
            value: async () => {
              syncAttempted = true;
              const error = new Error("injected copied-file sync failure");
              error.code = "EIO";
              throw error;
            },
          });
        }
        return handle;
      },
    },
    () =>
      assertStagingFailure(
        stagingOptions(fixture, { targets: [target] }),
      ),
  );

  assert.equal(syncAttempted, true);
  assert.equal(await pathExists(path.join(fixture.outputRoot, ".complete")), false);
});

test("fails closed when syncing a descendant directory fails before marker publication", async () => {
  const fixture = await stagingFixture();
  const target = TARGETS[2];
  const stagedBinRoot = path.join(fixture.outputRoot, target.key, "bin");
  const originalOpen = mutableFsPromises.open;
  let syncAttempted = false;

  await withFsPromisePatches(
    {
      open: async (filename, ...argumentsAfterFilename) => {
        const handle = await originalOpen(filename, ...argumentsAfterFilename);
        if (filename === stagedBinRoot) {
          Object.defineProperty(handle, "sync", {
            configurable: true,
            value: async () => {
              syncAttempted = true;
              const error = new Error("injected descendant-directory sync failure");
              error.code = "EIO";
              throw error;
            },
          });
        }
        return handle;
      },
    },
    () =>
      assertStagingFailure(
        stagingOptions(fixture, { targets: [target] }),
      ),
  );

  assert.equal(syncAttempted, true);
  assert.equal(await pathExists(path.join(fixture.outputRoot, ".complete")), false);
});

test("rejects an intermediate source directory replaced after validation", async () => {
  const fixture = await stagingFixture();
  const sourceDirectory = path.join(fixture.repositoryRoot, "npm", "launcher", "bin");
  const replacementDirectory = path.join(fixture.fixtureRoot, "replacement-launcher-bin");
  const capturedDirectory = path.join(fixture.fixtureRoot, "captured-launcher-bin");
  await mkdir(replacementDirectory, { mode: 0o700 });
  await copyFile(
    path.join(sourceDirectory, "ai-cli-gateway.js"),
    path.join(replacementDirectory, "ai-cli-gateway.js"),
  );
  await chmod(path.join(replacementDirectory, "ai-cli-gateway.js"), 0o755);
  const originalMkdir = mutableFsPromises.mkdir;
  const originalRename = mutableFsPromises.rename;
  let replaced = false;

  await withFsPromisePatches(
    {
      mkdir: async (directory, options) => {
        const result = await originalMkdir(directory, options);
        if (
          !replaced &&
          path.basename(directory) === "launcher" &&
          (path.dirname(directory) === fixture.outputRoot ||
            path.dirname(directory).startsWith(
              path.join(fixture.outputParent, `.${path.basename(fixture.outputRoot)}-`),
            ))
        ) {
          await originalRename(sourceDirectory, capturedDirectory);
          await originalRename(replacementDirectory, sourceDirectory);
          replaced = true;
        }
        return result;
      },
    },
    () => assertStagingFailure(stagingOptions(fixture)),
  );

  assert.equal(replaced, true);
  assert.equal(await pathExists(fixture.outputRoot), true);
  assert.equal(await pathExists(path.join(fixture.outputRoot, ".complete")), false);
  assert.equal(await pathExists(capturedDirectory), true);
  await assertNoOwnedTemporaryRoot(fixture);
});

test("rejects unsafe launcher source bytes before staging", async (t) => {
  await t.test("entry", async () => {
    const fixture = await stagingFixture();
    const entry = path.join(
      fixture.repositoryRoot,
      "npm",
      "launcher",
      "bin",
      "ai-cli-gateway.js",
    );
    const original = await readFile(entry, "utf8");
    await writeFile(entry, original.replace("slice(2)", "slice(1)"), "utf8");
    await chmod(entry, 0o755);

    await assertStagingFailure(stagingOptions(fixture));
    assert.equal(await pathExists(fixture.outputRoot), false);
  });

  await t.test("implementation", async () => {
    const fixture = await stagingFixture();
    const implementation = path.join(
      fixture.repositoryRoot,
      "npm",
      "launcher",
      "lib",
      "launcher.js",
    );
    const original = await readFile(implementation, "utf8");
    const unsafe = original.replace("shell: false", "shell: true ");
    assert.equal(unsafe.length, original.length);
    await writeFile(implementation, unsafe, "utf8");

    await assertStagingFailure(stagingOptions(fixture));
    assert.equal(await pathExists(fixture.outputRoot), false);
  });
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
    [".complete", "launcher", target.key].sort(),
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
  const script = path.join(fixtureRoot, `fake npm ${mutation}.js`);
  const startedMarker = `${script}.started`;
  const scratchMarker = `${script}.scratch`;
  const capturedHome = `${script}.captured-home`;
  const capturedCache = `${script}.captured-cache`;
  const capturedConfig = `${script}.captured-config`;
  const source = `const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");

const mutation = ${JSON.stringify(mutation)};
const startedMarker = ${JSON.stringify(startedMarker)};
const scratchMarker = ${JSON.stringify(scratchMarker)};
const capturedHome = ${JSON.stringify(capturedHome)};
const capturedCache = ${JSON.stringify(capturedCache)};
const capturedConfig = ${JSON.stringify(capturedConfig)};
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

fs.writeFileSync(startedMarker, "started\\n", { flag: "a" });
fs.writeFileSync(scratchMarker, process.env.HOME + "\\n");
if (mutation === "replace-npm-home" && !fs.existsSync(capturedHome)) {
  fs.renameSync(process.env.HOME, capturedHome);
  fs.mkdirSync(process.env.HOME, { mode: 0o700 });
  fs.writeFileSync(path.join(process.env.HOME, "foreign-owner-marker"), "foreign owner\\n");
}
if (mutation === "replace-npm-cache" && !fs.existsSync(capturedCache)) {
  fs.renameSync(process.env.NPM_CONFIG_CACHE, capturedCache);
  fs.mkdirSync(process.env.NPM_CONFIG_CACHE, { mode: 0o700 });
  fs.writeFileSync(
    path.join(process.env.NPM_CONFIG_CACHE, "foreign-owner-marker"),
    "foreign owner\\n",
  );
}
if (mutation === "redirect-npm-config" && !fs.existsSync(capturedConfig)) {
  fs.renameSync(process.env.NPM_CONFIG_USERCONFIG, capturedConfig);
  fs.symlinkSync(capturedConfig, process.env.NPM_CONFIG_USERCONFIG);
}
if (mutation === "extra-staging-entry") {
  const unexpected = path.join(path.dirname(process.cwd()), "unexpected-final-entry");
  if (!fs.existsSync(unexpected)) fs.writeFileSync(unexpected, "unexpected\\n");
}
if (mutation === "hang") setInterval(() => {}, 1_000);
if (mutation === "oversized-stdout") process.stdout.write("x".repeat(1024 * 1024 + 1));
if (mutation === "oversized-stderr") process.stderr.write("x".repeat(1024 * 1024 + 1));
if (mutation === "nonzero-exit") process.exit(37);
if (mutation === "signal-exit") process.kill(process.pid, "SIGTERM");

const packageRoot = process.cwd();
const manifest = JSON.parse(fs.readFileSync(path.join(packageRoot, "package.json"), "utf8"));
if (mutation === "mutate-staged-native" && manifest.name !== "ai-cli-gateway") {
  const binRoot = path.join(packageRoot, "bin");
  const binaryPath = path.join(binRoot, fs.readdirSync(binRoot)[0]);
  const original = fs.readFileSync(binaryPath);
  const replacement = Buffer.alloc(original.length, 0x78);
  if (replacement.equals(original)) replacement.fill(0x79);
  fs.writeFileSync(binaryPath, replacement);
}
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
      const executablePath = manifest.name === "ai-cli-gateway"
        ? "bin/ai-cli-gateway.js"
        : manifest.os?.[0] === "win32"
          ? undefined
          : "bin/ai-cli-gateway";
      files.push({
        path: relative,
        size: metadata.size,
        mode: relative === executablePath ? 0o755 : 0o644,
      });
    }
  }
}
visit(packageRoot);

const filename = manifest.name + "-" + manifest.version + ".tgz";
const tarball = Buffer.from("verified fake tarball for " + manifest.name + "\\n");
const tarballPath = path.join(args[4], filename);
if (mutation === "replace-earlier-tarball") {
  const earlier = fs.readdirSync(args[4]).filter((entry) => entry.endsWith(".tgz")).sort()[0];
  if (earlier !== undefined) {
    fs.writeFileSync(path.join(args[4], earlier), Buffer.from("tampered after first hash\\n"));
  }
}
if (mutation === "linked-tarball") {
  const outside = path.join(path.dirname(args[4]), "outside-" + filename);
  fs.writeFileSync(outside, tarball);
  fs.linkSync(outside, tarballPath);
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
  await writeFile(script, source, { mode: 0o644 });
  return {
    capturedCache,
    capturedConfig,
    capturedHome,
    scratchMarker,
    script,
    startedMarker,
  };
}

async function packFixture({ mutation = "none", targets = [TARGETS[2]] } = {}) {
  const fixture = await stagingFixture();
  await stagePackages(stagingOptions(fixture, { targets }));
  const tarballRoot = path.join(fixture.fixtureRoot, "tarballs");
  await mkdir(tarballRoot, { mode: 0o700 });
  const descriptor = path.join(tarballRoot, "packages.json");
  const fakeNpm = await writeFakeNpm(fixture.fixtureRoot, mutation);
  return {
    ...fixture,
    descriptor,
    npmExecutable: process.execPath,
    npmScript: fakeNpm.script,
    npmStartedMarker: fakeNpm.startedMarker,
    npmScratchMarker: fakeNpm.scratchMarker,
    npmCapturedCache: fakeNpm.capturedCache,
    npmCapturedConfig: fakeNpm.capturedConfig,
    npmCapturedHome: fakeNpm.capturedHome,
    tarballRoot,
    targets,
    options: {
      stagingRoot: fixture.outputRoot,
      tarballRoot,
      descriptor,
      version: PACKAGE_VERSION,
      npmExecutable: process.execPath,
      npmArguments: [fakeNpm.script],
    },
  };
}

async function assertVerificationFailure(options) {
  await assert.rejects(packAndVerify(options), {
    name: "Error",
    message: "npm package verification failed",
  });
}

async function withFinalWindowMutation(
  fixture,
  { asynchronous, synchronous },
  operation,
) {
  const originalPromiseLstat = mutableFsPromises.lstat;
  const originalSyncLstat = mutableFs.lstatSync;
  let injected = false;
  const descriptorExistsAsync = async () => {
    try {
      await originalPromiseLstat(fixture.descriptor);
      return true;
    } catch (error) {
      if (error?.code === "ENOENT") {
        return false;
      }
      throw error;
    }
  };
  const descriptorExistsSync = () => {
    try {
      originalSyncLstat(fixture.descriptor);
      return true;
    } catch (error) {
      if (error?.code === "ENOENT") {
        return false;
      }
      throw error;
    }
  };

  await withFsPatches(
    {
      lstatSync: (filename, options) => {
        if (
          filename === fixture.outputRoot &&
          !injected &&
          descriptorExistsSync()
        ) {
          injected = true;
          synchronous();
        }
        return originalSyncLstat(filename, options);
      },
    },
    () =>
      withFsPromisePatches(
        {
          lstat: async (filename, options) => {
            if (
              filename === fixture.outputRoot &&
              !injected &&
              (await descriptorExistsAsync())
            ) {
              injected = true;
              await asynchronous();
            }
            return originalPromiseLstat(filename, options);
          },
        },
        operation,
      ),
  );
  assert.equal(injected, true);
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

test("rejects an extra staging-root entry created during npm", async () => {
  const fixture = await packFixture({ mutation: "extra-staging-entry" });
  const unexpected = path.join(fixture.outputRoot, "unexpected-final-entry");

  await assertVerificationFailure(fixture.options);

  assert.equal(await readFile(unexpected, "utf8"), "unexpected\n");
});

test("final cohort rejects an extra tarball-root entry", async () => {
  const fixture = await packFixture();
  const unexpected = path.join(fixture.tarballRoot, "unexpected-final-entry");
  const originalWriteFile = mutableFsPromises.writeFile;
  const originalWriteFileSync = mutableFs.writeFileSync;

  await withFinalWindowMutation(
    fixture,
    {
      asynchronous: () =>
        originalWriteFile(unexpected, "unexpected\n", {
          flag: "wx",
          mode: 0o600,
        }),
      synchronous: () =>
        originalWriteFileSync(unexpected, "unexpected\n", {
          flag: "wx",
          mode: 0o600,
        }),
    },
    () => assertVerificationFailure(fixture.options),
  );

  assert.equal(await readFile(unexpected, "utf8"), "unexpected\n");
});

test("final cohort rejects a tarball replaced after its individual hash", async () => {
  const fixture = await packFixture();
  const target = fixture.targets[0];
  const tarball = path.join(
    fixture.tarballRoot,
    npmFilename(target.packageName, PACKAGE_VERSION),
  );
  const captured = path.join(fixture.fixtureRoot, "captured-final-tarball.tgz");
  const originalReadFile = mutableFsPromises.readFile;
  const originalRename = mutableFsPromises.rename;
  const originalWriteFile = mutableFsPromises.writeFile;
  const originalReadFileSync = mutableFs.readFileSync;
  const originalRenameSync = mutableFs.renameSync;
  const originalWriteFileSync = mutableFs.writeFileSync;

  await withFinalWindowMutation(
    fixture,
    {
      asynchronous: async () => {
        const content = await originalReadFile(tarball);
        await originalRename(tarball, captured);
        await originalWriteFile(tarball, content, { flag: "wx", mode: 0o644 });
      },
      synchronous: () => {
        const content = originalReadFileSync(tarball);
        originalRenameSync(tarball, captured);
        originalWriteFileSync(tarball, content, { flag: "wx", mode: 0o644 });
      },
    },
    () => assertVerificationFailure(fixture.options),
  );

  const [capturedIdentity, replacementIdentity] = await Promise.all([
    lstat(captured, { bigint: true }),
    lstat(tarball, { bigint: true }),
  ]);
  assert.notEqual(capturedIdentity.ino, replacementIdentity.ino);
  assert.deepEqual(await readFile(captured), await readFile(tarball));
});

test("final cohort rejects an earlier tarball rewritten in place while a later tarball hashes", async () => {
  const fixture = await packFixture();
  const earlierTarball = path.join(
    fixture.tarballRoot,
    npmFilename(fixture.targets[0].packageName, PACKAGE_VERSION),
  );
  const laterTarball = path.join(
    fixture.tarballRoot,
    npmFilename("ai-cli-gateway", PACKAGE_VERSION),
  );
  const originalFstatSync = mutableFs.fstatSync;
  const originalLstatSync = mutableFs.lstatSync;
  const originalReadFileSync = mutableFs.readFileSync;
  const originalReadSync = mutableFs.readSync;
  const originalUtimesSync = mutableFs.utimesSync;
  const originalWriteFileSync = mutableFs.writeFileSync;
  let beforeRewrite;
  let afterRewrite;
  let originalContent;
  let replacement;
  let rewritten = false;

  await withFsPatches(
    {
      readSync: (fd, ...argumentsAfterFd) => {
        if (!rewritten) {
          const opened = originalFstatSync(fd, { bigint: true });
          const laterIdentity = originalLstatSync(laterTarball, { bigint: true });
          if (
            opened.dev === laterIdentity.dev &&
            opened.ino === laterIdentity.ino
          ) {
            beforeRewrite = originalLstatSync(earlierTarball, { bigint: true });
            originalContent = originalReadFileSync(earlierTarball);
            replacement = Buffer.from(originalContent);
            replacement[0] ^= 0xff;
            originalWriteFileSync(earlierTarball, replacement);
            const forcedTimestamp = new Date("2000-01-01T00:00:00.000Z");
            originalUtimesSync(earlierTarball, forcedTimestamp, forcedTimestamp);
            afterRewrite = originalLstatSync(earlierTarball, { bigint: true });
            rewritten = true;
          }
        }
        return originalReadSync(fd, ...argumentsAfterFd);
      },
    },
    () => assertVerificationFailure(fixture.options),
  );

  assert.equal(rewritten, true);
  assert.equal(beforeRewrite.dev, afterRewrite.dev);
  assert.equal(beforeRewrite.ino, afterRewrite.ino);
  assert.equal(beforeRewrite.size, afterRewrite.size);
  assert.equal(originalContent.length, replacement.length);
  assert.equal(originalContent.equals(replacement), false);
});

test("final cohort rejects a descriptor replaced after its prior read", async () => {
  const fixture = await packFixture();
  const captured = path.join(fixture.fixtureRoot, "captured-final-packages.json");
  const originalReadFile = mutableFsPromises.readFile;
  const originalRename = mutableFsPromises.rename;
  const originalWriteFile = mutableFsPromises.writeFile;
  const originalReadFileSync = mutableFs.readFileSync;
  const originalRenameSync = mutableFs.renameSync;
  const originalWriteFileSync = mutableFs.writeFileSync;

  await withFinalWindowMutation(
    fixture,
    {
      asynchronous: async () => {
        const content = await originalReadFile(fixture.descriptor);
        await originalRename(fixture.descriptor, captured);
        await originalWriteFile(fixture.descriptor, content, {
          flag: "wx",
          mode: 0o644,
        });
      },
      synchronous: () => {
        const content = originalReadFileSync(fixture.descriptor);
        originalRenameSync(fixture.descriptor, captured);
        originalWriteFileSync(fixture.descriptor, content, {
          flag: "wx",
          mode: 0o644,
        });
      },
    },
    () => assertVerificationFailure(fixture.options),
  );

  const [capturedIdentity, replacementIdentity] = await Promise.all([
    lstat(captured, { bigint: true }),
    lstat(fixture.descriptor, { bigint: true }),
  ]);
  assert.notEqual(capturedIdentity.ino, replacementIdentity.ino);
  assert.deepEqual(await readFile(captured), await readFile(fixture.descriptor));
});

test("rejects an incomplete staging root without the exact completion marker", async () => {
  const fixture = await packFixture();
  await rm(path.join(fixture.outputRoot, ".complete"), { force: true });

  await assertVerificationFailure(fixture.options);

  assert.equal(await pathExists(fixture.descriptor), false);
});

test("revalidates every earlier tarball after later npm executions", async () => {
  const fixture = await packFixture({ mutation: "replace-earlier-tarball" });

  await assertVerificationFailure(fixture.options);

  assert.equal(await pathExists(fixture.npmStartedMarker), true);
  assert.equal(await pathExists(fixture.descriptor), false);
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

test("rejects a hard-linked tarball on every platform", async () => {
  const fixture = await packFixture({ mutation: "linked-tarball" });
  await assertVerificationFailure(fixture.options);
  assert.equal(await pathExists(fixture.npmStartedMarker), true);
  assert.equal(await pathExists(fixture.descriptor), false);
});

test("rejects lifecycle scripts in the staged launcher manifest", async () => {
  const fixture = await packFixture();
  const manifestPath = path.join(fixture.outputRoot, "launcher", "package.json");
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  manifest.scripts = { prepack: "exit 99" };
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, "utf8");

  await assertVerificationFailure(fixture.options);
  assert.equal(await pathExists(fixture.descriptor), false);
});

test("rejects unsafe same-size staged launcher implementation bytes", async () => {
  const fixture = await packFixture();
  const implementation = path.join(
    fixture.outputRoot,
    "launcher",
    "lib",
    "launcher.js",
  );
  const original = await readFile(implementation, "utf8");
  const unsafe = original.replace("shell: false", "shell: true ");
  assert.equal(unsafe.length, original.length);
  await writeFile(implementation, unsafe, "utf8");

  await assertVerificationFailure(fixture.options);

  assert.equal(await pathExists(fixture.descriptor), false);
});

test("rejects a same-inode same-size native mutation during npm pack", async () => {
  const fixture = await packFixture({ mutation: "mutate-staged-native" });

  await assertVerificationFailure(fixture.options);

  assert.equal(await pathExists(fixture.npmStartedMarker), true);
  assert.equal(await pathExists(fixture.descriptor), false);
});

test("rejects a child directory swapped after its parent-observed identity", async () => {
  const fixture = await packFixture();
  const target = fixture.targets[0];
  const packageRoot = path.join(fixture.outputRoot, target.key);
  const binRoot = path.join(packageRoot, "bin");
  const capturedBinRoot = path.join(fixture.fixtureRoot, "captured-staged-bin");
  const replacementBinRoot = path.join(fixture.fixtureRoot, "replacement-staged-bin");
  await mkdir(replacementBinRoot, { mode: 0o700 });
  await copyFile(
    path.join(binRoot, target.executable),
    path.join(replacementBinRoot, target.executable),
  );
  await chmod(
    path.join(replacementBinRoot, target.executable),
    target.platform === "win32" ? 0o644 : 0o755,
  );
  const originalLstat = mutableFsPromises.lstat;
  const originalRename = mutableFsPromises.rename;
  let swapped = false;

  await withFsPromisePatches(
    {
      lstat: async (filename, options) => {
        const observed = await originalLstat(filename, options);
        if (filename === binRoot && !swapped) {
          await originalRename(binRoot, capturedBinRoot);
          await originalRename(replacementBinRoot, binRoot);
          swapped = true;
        }
        return observed;
      },
    },
    () => assertVerificationFailure(fixture.options),
  );

  assert.equal(swapped, true);
  assert.equal(await pathExists(capturedBinRoot), true);
  assert.equal(await pathExists(fixture.descriptor), false);
});

test("never overwrites a descriptor raced during publication", async () => {
  const fixture = await packFixture();
  const originalLink = mutableFsPromises.link;
  const originalLstat = mutableFsPromises.lstat;
  const originalOpen = mutableFsPromises.open;
  const originalRename = mutableFsPromises.rename;
  const originalWriteFile = mutableFsPromises.writeFile;
  let racedIdentity;
  const publishRacedDescriptor = async () => {
    await originalWriteFile(fixture.descriptor, "attacker descriptor\n", {
      flag: "wx",
      mode: 0o600,
    });
    racedIdentity = await originalLstat(fixture.descriptor, { bigint: true });
  };

  await withFsPromisePatches(
    {
      open: async (filename, flags, ...argumentsAfterFlags) => {
        if (
          filename === fixture.descriptor &&
          (flags & constants.O_CREAT) !== 0 &&
          racedIdentity === undefined
        ) {
          await publishRacedDescriptor();
        }
        return originalOpen(filename, flags, ...argumentsAfterFlags);
      },
      link: async (source, destination) => {
        if (destination === fixture.descriptor && racedIdentity === undefined) {
          await publishRacedDescriptor();
        }
        return originalLink(source, destination);
      },
      rename: async (source, destination) => {
        if (destination === fixture.descriptor && racedIdentity === undefined) {
          await publishRacedDescriptor();
        }
        return originalRename(source, destination);
      },
    },
    () => assertVerificationFailure(fixture.options),
  );

  assert.ok(racedIdentity);
  assert.equal(await readFile(fixture.descriptor, "utf8"), "attacker descriptor\n");
  const actual = await lstat(fixture.descriptor, { bigint: true });
  assert.equal(actual.dev, racedIdentity.dev);
  assert.equal(actual.ino, racedIdentity.ino);
});

test("never deletes a descriptor replacement raced during cleanup", async () => {
  const fixture = await packFixture();
  const capturedDescriptor = path.join(
    fixture.fixtureRoot,
    "captured-cleanup-packages.json",
  );
  const originalOpen = mutableFsPromises.open;
  const originalRename = mutableFsPromises.rename;
  const originalUnlink = mutableFsPromises.unlink;
  const originalWriteFile = mutableFsPromises.writeFile;
  let raceMode;

  await withFsPromisePatches(
    {
      open: async (filename, flags, ...argumentsAfterFlags) => {
        const handle = await originalOpen(filename, flags, ...argumentsAfterFlags);
        if (
          filename === fixture.descriptor &&
          (flags & constants.O_CREAT) !== 0 &&
          raceMode === undefined
        ) {
          await originalRename(fixture.descriptor, capturedDescriptor);
          await originalWriteFile(fixture.descriptor, "foreign owner\n", {
            flag: "wx",
            mode: 0o600,
          });
          raceMode = "direct-publication";
        }
        return handle;
      },
      unlink: async (filename) => {
        if (
          path.dirname(filename) === fixture.tarballRoot &&
          path.basename(filename).startsWith(".packages.json-") &&
          path.basename(filename).endsWith(".tmp")
        ) {
          await originalUnlink(filename);
          throw new Error("injected temporary-descriptor unlink failure");
        }
        if (filename === fixture.descriptor && raceMode === undefined) {
          await originalRename(fixture.descriptor, capturedDescriptor);
          await originalWriteFile(fixture.descriptor, "foreign owner\n", {
            flag: "wx",
            mode: 0o600,
          });
          raceMode = "cleanup";
        }
        return originalUnlink(filename);
      },
    },
    () => assertVerificationFailure(fixture.options),
  );

  assert.ok(raceMode);
  assert.equal(await pathExists(capturedDescriptor), true);
  assert.equal(await readFile(fixture.descriptor, "utf8"), "foreign owner\n");
});

test("supports an absolute Node executable with one fixed absolute npm script prefix", async () => {
  const fixture = await packFixture();

  const descriptors = await packAndVerify({
    ...fixture.options,
    npmExecutable: process.execPath,
    npmArguments: [fixture.npmScript],
  });

  assert.deepEqual(descriptors.map(({ name }) => name), [
    fixture.targets[0].packageName,
    "ai-cli-gateway",
  ]);
  assert.equal(await pathExists(fixture.npmStartedMarker), true);
});

test(
  "rejects unsafe npm scratch parents before scratch creation or npm execution",
  { skip: process.platform === "win32" },
  async (t) => {
    for (const [label, unsafeMode] of [
      ["world-writable", 0o777],
      ["sticky world-writable", 0o1777],
      ["group-writable", 0o770],
    ]) {
      await t.test(label, async () => {
        const fixture = await packFixture();
        await chmod(fixture.fixtureRoot, unsafeMode);
        let failure;

        try {
          await packAndVerify(fixture.options);
        } catch (error) {
          failure = error;
        }

        assert.equal(await pathExists(fixture.npmStartedMarker), false);
        assert.equal(await pathExists(fixture.npmScratchMarker), false);
        assert.equal(
          (await readdir(fixture.fixtureRoot)).some((entry) =>
            entry.startsWith(".npm-pack-home-")),
          false,
        );
        assert.deepEqual(
          failure === undefined
            ? undefined
            : { message: failure.message, name: failure.name },
          { message: "npm package verification failed", name: "Error" },
        );
      });
    }
  },
);

test("retains the private npm scratch root after successful verification", async () => {
  const fixture = await packFixture();

  await packAndVerify(fixture.options);

  const scratchRoot = (await readFile(fixture.npmScratchMarker, "utf8")).trim();
  assert.equal(path.isAbsolute(scratchRoot), true);
  assert.equal(await pathExists(scratchRoot), true);
  assert.deepEqual(
    (await readdir(scratchRoot)).sort(),
    ["cache", "global.npmrc", "logs", "tmp", "user.npmrc"].sort(),
  );
  await assertMode(scratchRoot, 0o700);
});

test("rejects npm scratch replacement and redirection without deleting it", async (t) => {
  for (const mutation of [
    "replace-npm-home",
    "replace-npm-cache",
    "redirect-npm-config",
  ]) {
    await t.test(mutation, async () => {
      const fixture = await packFixture({ mutation });

      await assertVerificationFailure(fixture.options);

      const scratchRoot = (await readFile(fixture.npmScratchMarker, "utf8")).trim();
      if (mutation === "replace-npm-home") {
        assert.equal(await pathExists(fixture.npmCapturedHome), true);
        assert.equal(
          await readFile(path.join(scratchRoot, "foreign-owner-marker"), "utf8"),
          "foreign owner\n",
        );
      } else if (mutation === "replace-npm-cache") {
        assert.equal(await pathExists(fixture.npmCapturedCache), true);
        assert.equal(
          await readFile(
            path.join(scratchRoot, "cache", "foreign-owner-marker"),
            "utf8",
          ),
          "foreign owner\n",
        );
      } else {
        assert.equal(await pathExists(fixture.npmCapturedConfig), true);
        assert.equal(
          (await lstat(path.join(scratchRoot, "user.npmrc"))).isSymbolicLink(),
          true,
        );
      }
    });
  }
});

test("bounds npm child timeout, output, exit, and signal failures", async (t) => {
  const cases = [
    ["hang", { npmTimeoutMs: 750 }],
    ["oversized-stdout", {}],
    ["oversized-stderr", {}],
    ["nonzero-exit", {}],
    ["signal-exit", {}],
  ];
  for (const [mutation, overrides] of cases) {
    await t.test(mutation, async () => {
      const fixture = await packFixture({ mutation });
      await assertVerificationFailure({ ...fixture.options, ...overrides });
      assert.equal(await pathExists(fixture.npmStartedMarker), true);
      assert.equal(await pathExists(fixture.descriptor), false);
    });
  }
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

test("source check rejects unsafe same-size launcher implementation bytes", async () => {
  const fixture = await stagingFixture();
  const implementation = path.join(
    fixture.repositoryRoot,
    "npm",
    "launcher",
    "lib",
    "launcher.js",
  );
  const original = await readFile(implementation, "utf8");
  const unsafe = original.replace("shell: false", "shell: true ");
  assert.equal(unsafe.length, original.length);
  await writeFile(implementation, unsafe, "utf8");

  await assert.rejects(
    sourceCheck({ repositoryRoot: fixture.repositoryRoot, version: PACKAGE_VERSION }),
    { name: "Error", message: "npm package verification failed" },
  );
});

test("source check revalidates launcher bytes in its final pass", async (t) => {
  for (const relative of [
    "bin/ai-cli-gateway.js",
    "lib/launcher.js",
  ]) {
    await t.test(relative, async () => {
      const fixture = await stagingFixture();
      const launcherRoot = path.join(fixture.repositoryRoot, "npm", "launcher");
      const filename = path.join(launcherRoot, ...relative.split("/"));
      const original = await readFile(filename);
      const replacement = Buffer.from(original);
      replacement[0] ^= 0x01;
      assert.equal(replacement.length, original.length);
      const originalLstat = mutableFsPromises.lstat;
      const originalWriteFile = mutableFsPromises.writeFile;
      let launcherRootLstats = 0;
      let mutated = false;

      await withFsPromisePatches(
        {
          lstat: async (candidate, options) => {
            const metadata = await originalLstat(candidate, options);
            if (candidate === launcherRoot) {
              launcherRootLstats += 1;
              if (launcherRootLstats === 7 && !mutated) {
                await originalWriteFile(filename, replacement);
                mutated = true;
              }
            }
            return metadata;
          },
        },
        () =>
          assert.rejects(
            sourceCheck({
              repositoryRoot: fixture.repositoryRoot,
              version: PACKAGE_VERSION,
            }),
            { name: "Error", message: "npm package verification failed" },
          ),
      );

      assert.equal(mutated, true);
    });
  }
});

test(
  "source check validates ownership beyond the repository root when supported",
  { skip: typeof process.getuid !== "function" },
  async () => {
    const fixture = await stagingFixture();
    const originalGetuid = process.getuid;
    const currentUid = originalGetuid();
    let calls = 0;
    process.getuid = () => {
      calls += 1;
      return calls === 1 ? currentUid : currentUid + 1;
    };
    try {
      await assert.rejects(
        sourceCheck({ repositoryRoot: fixture.repositoryRoot, version: PACKAGE_VERSION }),
        { name: "Error", message: "npm package verification failed" },
      );
    } finally {
      process.getuid = originalGetuid;
    }
    assert.ok(calls > 1);
  },
);

function hostTarget() {
  const target = TARGETS.find(
    ({ platform, arch }) => platform === process.platform && arch === process.arch,
  );
  assert.ok(target, `test host ${process.platform}-${process.arch} must be supported`);
  return target;
}

async function installedNpmOptions() {
  if (process.platform === "win32") {
    return {
      npmArguments: [
        path.join(
          path.dirname(process.execPath),
          "node_modules",
          "npm",
          "bin",
          "npm-cli.js",
        ),
      ],
      npmExecutable: process.execPath,
    };
  }
  return {
    npmExecutable: await realpath(path.join(path.dirname(process.execPath), "npm")),
  };
}

test(
  "real npm pack is reproducible for the staged host native package and launcher",
  async () => {
    const target = hostTarget();
    const npmOptions = await installedNpmOptions();
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
        ...npmOptions,
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
