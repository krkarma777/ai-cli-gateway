import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import {
  chmod,
  copyFile,
  mkdir,
  mkdtemp,
  readFile,
  realpath,
  rename,
  rm,
  symlink,
  stat,
  unlink,
  writeFile,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { after, test } from "node:test";
import { fileURLToPath } from "node:url";

import {
  LauncherError,
  resolveNative,
  targetFor,
} from "../launcher/lib/launcher.js";

const launcherVersion = "0.2.1";
const temporaryRoots = [];
const npmRoot = path.dirname(fileURLToPath(new URL("../package.json", import.meta.url)));
const sourceLauncherRoot = path.join(npmRoot, "launcher");

after(async () => {
  await Promise.all(
    temporaryRoots.splice(0).map((root) => rm(root, { force: true, recursive: true })),
  );
});

function hostTarget() {
  const target = targetFor(process.platform, process.arch);
  assert.ok(target, `test host ${process.platform}-${process.arch} must be supported`);
  return target;
}

async function writeJson(filename, value) {
  await writeFile(filename, JSON.stringify(value), "utf8");
}

async function installedFixture({
  launcherName = "ai-cli-gateway",
  launcherVersion: fixtureLauncherVersion = launcherVersion,
  nativeName,
  nativeVersion = fixtureLauncherVersion,
} = {}) {
  const target = hostTarget();
  const fixtureRoot = await mkdtemp(path.join(os.tmpdir(), "ai-cli-gateway-launcher-"));
  temporaryRoots.push(fixtureRoot);

  const launcherRoot = path.join(fixtureRoot, "launcher");
  const nativeRoot = path.join(launcherRoot, "node_modules", target.packageName);
  const binary = path.join(nativeRoot, "bin", target.executable);

  await mkdir(path.join(launcherRoot, "lib"), { recursive: true });
  await mkdir(path.dirname(binary), { recursive: true });
  await writeJson(path.join(launcherRoot, "package.json"), {
    name: launcherName,
    version: fixtureLauncherVersion,
  });
  await writeJson(path.join(nativeRoot, "package.json"), {
    name: nativeName ?? target.packageName,
    version: nativeVersion,
  });
  await copyFile(process.execPath, binary);
  await chmod(binary, 0o755);

  return { binary, fixtureRoot, launcherRoot, nativeRoot, target };
}

async function replaceBinaryWithSymlink(binary) {
  const realBinary = `${binary}.real`;
  await rename(binary, realBinary);
  await symlink(path.basename(realBinary), binary, "file");
}

async function runnableFixture(source) {
  const fixture = await installedFixture();
  const entry = path.join(fixture.launcherRoot, "bin", "ai-cli-gateway.js");
  const program = path.join(fixture.fixtureRoot, "native-program.js");

  await copyFile(path.join(sourceLauncherRoot, "package.json"), path.join(fixture.launcherRoot, "package.json"));
  await copyFile(path.join(sourceLauncherRoot, "lib", "launcher.js"), path.join(fixture.launcherRoot, "lib", "launcher.js"));
  await mkdir(path.dirname(entry), { recursive: true });
  await copyFile(path.join(sourceLauncherRoot, "bin", "ai-cli-gateway.js"), entry);
  await chmod(entry, 0o755);
  await writeFile(program, source, "utf8");

  return { ...fixture, entry, program };
}

function installedCommand(fixture, args = []) {
  if (process.platform === "win32") {
    return { command: process.execPath, args: [fixture.entry, fixture.program, ...args] };
  }
  return { command: fixture.entry, args: [fixture.program, ...args] };
}

function runInstalled(fixture, args = [], options = {}) {
  const command = installedCommand(fixture, args);
  return spawnSync(command.command, command.args, {
    encoding: "utf8",
    shell: false,
    ...options,
  });
}

function runInstalledAsync(fixture, args = []) {
  const command = installedCommand(fixture, args);
  return spawn(command.command, command.args, {
    shell: false,
    stdio: ["ignore", "pipe", "pipe"],
  });
}

function waitForMarker(child, readOutput, marker) {
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error(`timed out waiting for ${marker}`));
    }, 10_000);

    const inspect = () => {
      if (readOutput().includes(marker)) {
        clearTimeout(timeout);
        child.stdout.off("data", inspect);
        child.off("error", failed);
        child.off("exit", exited);
        resolve();
      }
    };
    const failed = (error) => {
      clearTimeout(timeout);
      child.stdout.off("data", inspect);
      child.off("exit", exited);
      reject(error);
    };
    const exited = (code, signal) => {
      clearTimeout(timeout);
      child.stdout.off("data", inspect);
      child.off("error", failed);
      reject(new Error(`wrapper exited before ${marker}: ${code ?? signal}`));
    };

    child.stdout.on("data", inspect);
    child.once("error", failed);
    child.once("exit", exited);
    inspect();
  });
}

function waitForClose(child) {
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error("timed out waiting for wrapper exit"));
    }, 10_000);
    child.once("error", (error) => {
      clearTimeout(timeout);
      reject(error);
    });
    child.once("close", (code, signal) => {
      clearTimeout(timeout);
      resolve({ code, signal });
    });
  });
}

async function assertInvalidNative(fixture) {
  await assert.rejects(
    resolveNative({
      launcherRoot: fixture.launcherRoot,
      platform: process.platform,
      arch: process.arch,
    }),
    (error) => {
      assert.ok(error instanceof LauncherError);
      assert.equal(error.code, "INVALID_NATIVE");
      assert.equal(error.message, "ai-cli-gateway: native package installation is invalid");
      assert.equal(error.message.includes(fixture.fixtureRoot), false);
      return true;
    },
  );
}

test("selects all five exact platform packages", () => {
  assert.equal(targetFor("darwin", "x64").packageName, "ai-cli-gateway-darwin-x64");
  assert.equal(targetFor("darwin", "arm64").packageName, "ai-cli-gateway-darwin-arm64");
  assert.equal(targetFor("linux", "x64").packageName, "ai-cli-gateway-linux-x64");
  assert.equal(targetFor("linux", "arm64").packageName, "ai-cli-gateway-linux-arm64");
  assert.equal(targetFor("win32", "x64").packageName, "ai-cli-gateway-win32-x64");
  assert.equal(targetFor("freebsd", "x64"), undefined);
});

test("resolves a valid installed native executable", async () => {
  const fixture = await installedFixture();
  const resolved = await resolveNative({
    launcherRoot: fixture.launcherRoot,
    platform: process.platform,
    arch: process.arch,
  });

  assert.deepEqual(resolved, {
    binary: await realpath(fixture.binary),
    version: launcherVersion,
  });
});

test("rejects an invalid launcher package name", async () => {
  const fixture = await installedFixture({ launcherName: "not-ai-cli-gateway" });
  await assert.rejects(
    resolveNative({
      launcherRoot: fixture.launcherRoot,
      platform: process.platform,
      arch: process.arch,
    }),
    {
      name: "LauncherError",
      code: "INVALID_LAUNCHER",
      message: "ai-cli-gateway: launcher installation is invalid",
    },
  );
});

test("rejects a noncanonical launcher version", async () => {
  const fixture = await installedFixture({ launcherVersion: "01.2.3" });
  await assert.rejects(
    resolveNative({
      launcherRoot: fixture.launcherRoot,
      platform: process.platform,
      arch: process.arch,
    }),
    {
      name: "LauncherError",
      code: "INVALID_LAUNCHER",
      message: "ai-cli-gateway: launcher installation is invalid",
    },
  );
});

test("rejects an unsupported platform with the exact sanitized message", async () => {
  const fixture = await installedFixture();
  await assert.rejects(
    resolveNative({ launcherRoot: fixture.launcherRoot, platform: "freebsd", arch: "x64" }),
    {
      name: "LauncherError",
      code: "UNSUPPORTED_PLATFORM",
      message:
        'ai-cli-gateway: unsupported platform "freebsd-x64"; supported: darwin-x64, darwin-arm64, linux-x64, linux-arm64, win32-x64',
    },
  );
});

test("redacts invalid runtime values in the unsupported platform message", async () => {
  const fixture = await installedFixture();
  await assert.rejects(
    resolveNative({
      launcherRoot: fixture.launcherRoot,
      platform: 'linux"\n/private/leak',
      arch: "x64!",
    }),
    {
      name: "LauncherError",
      code: "UNSUPPORTED_PLATFORM",
      message:
        'ai-cli-gateway: unsupported platform "unknown-unknown"; supported: darwin-x64, darwin-arm64, linux-x64, linux-arm64, win32-x64',
    },
  );
});

test("reports a missing optional package with the exact reinstall guidance", async () => {
  const fixture = await installedFixture();
  await rm(fixture.nativeRoot, { recursive: true });

  await assert.rejects(
    resolveNative({
      launcherRoot: fixture.launcherRoot,
      platform: process.platform,
      arch: process.arch,
    }),
    {
      name: "LauncherError",
      code: "MISSING_NATIVE",
      message: `ai-cli-gateway: native package ${fixture.target.packageName}@${launcherVersion} is missing; reinstall with "npm install --global ai-cli-gateway@${launcherVersion}" without --omit=optional`,
    },
  );
});

test("rejects a native package with the wrong name", async () => {
  const fixture = await installedFixture({ nativeName: "not-the-native-package" });
  await assertInvalidNative(fixture);
});

test("rejects a native package version mismatch", async () => {
  const fixture = await installedFixture({ nativeVersion: "0.2.0" });
  await assert.rejects(
    resolveNative({ launcherRoot: fixture.launcherRoot, platform: process.platform, arch: process.arch }),
    { code: "INVALID_NATIVE" },
  );
});

test("rejects a missing native executable", async () => {
  const fixture = await installedFixture();
  await unlink(fixture.binary);
  await assertInvalidNative(fixture);
});

test("rejects a directory in place of the native executable", async () => {
  const fixture = await installedFixture();
  await unlink(fixture.binary);
  await mkdir(fixture.binary);
  await assertInvalidNative(fixture);
});

test("rejects a native executable containment escape", async () => {
  const fixture = await installedFixture();
  const packageBin = path.dirname(fixture.binary);
  const outsideBin = path.join(fixture.fixtureRoot, "outside-bin");
  const outsideBinary = path.join(outsideBin, fixture.target.executable);

  await rm(packageBin, { recursive: true });
  await mkdir(outsideBin);
  await copyFile(process.execPath, outsideBinary);
  await chmod(outsideBinary, 0o755);
  await symlink(outsideBin, packageBin, process.platform === "win32" ? "junction" : "dir");

  await assertInvalidNative(fixture);
});

test("rejects a native executable without POSIX execute bits", async (t) => {
  if (process.platform === "win32") {
    t.skip("Windows does not use POSIX execute bits");
    return;
  }
  const fixture = await installedFixture();
  await chmod(fixture.binary, 0o644);
  await assertInvalidNative(fixture);
});

test("rejects a linked native executable", async (t) => {
  if (process.platform === "win32") {
    t.skip("Windows link policy uses the real-file contract");
    return;
  }
  const fixture = await installedFixture();
  await replaceBinaryWithSymlink(fixture.binary);
  await assert.rejects(
    resolveNative({ launcherRoot: fixture.launcherRoot, platform: process.platform, arch: process.arch }),
    { code: "INVALID_NATIVE" },
  );
});

test("does not leak filesystem paths from malformed native metadata", async () => {
  const fixture = await installedFixture();
  await writeFile(path.join(fixture.nativeRoot, "package.json"), "not-json", "utf8");

  await assertInvalidNative(fixture);
});

test("launcher bin has the exact executable entry", async () => {
  const entry = path.join(sourceLauncherRoot, "bin", "ai-cli-gateway.js");
  if (process.platform !== "win32") {
    assert.equal((await stat(entry)).mode & 0o777, 0o755);
  }
  assert.equal(
    await readFile(entry, "utf8"),
    '#!/usr/bin/env node\nimport { main } from "../lib/launcher.js";\n\nawait main(process.argv.slice(2));\n',
  );
});

test("preserves argument boundaries and numeric exit status", async () => {
  const fixture = await runnableFixture(
    'process.stdout.write(JSON.stringify(process.argv.slice(2))); process.exit(23);',
  );
  const args = ["space value", "quote'\"", "$(not-a-shell)"];
  const result = await runInstalled(fixture, args);
  assert.equal(result.status, 23);
  assert.equal(result.stdout, JSON.stringify(args));
  assert.equal(result.stderr, "");
});

test("inherits native stdout", async () => {
  const fixture = await runnableFixture('process.stdout.write("native stdout");');
  const result = runInstalled(fixture);

  assert.equal(result.status, 0);
  assert.equal(result.stdout, "native stdout");
  assert.equal(result.stderr, "");
});

test("inherits native stderr", async () => {
  const fixture = await runnableFixture('process.stderr.write("native stderr");');
  const result = runInstalled(fixture);

  assert.equal(result.status, 0);
  assert.equal(result.stdout, "");
  assert.equal(result.stderr, "native stderr");
});

test("inherits native stdin", async () => {
  const fixture = await runnableFixture("process.stdin.pipe(process.stdout);");
  const result = runInstalled(fixture, [], { input: "native stdin" });

  assert.equal(result.status, 0);
  assert.equal(result.stdout, "native stdin");
  assert.equal(result.stderr, "");
});

test("prints only the missing-native launcher error", async () => {
  const fixture = await runnableFixture("");
  await rm(fixture.nativeRoot, { recursive: true });
  const result = runInstalled(fixture);

  assert.equal(result.status, 1);
  assert.equal(result.stdout, "");
  assert.equal(
    result.stderr,
    `ai-cli-gateway: native package ${fixture.target.packageName}@${launcherVersion} is missing; reinstall with "npm install --global ai-cli-gateway@${launcherVersion}" without --omit=optional\n`,
  );
  assert.equal(result.stderr.includes(fixture.fixtureRoot), false);
});

test("translates a real child spawn error and removes signal listeners", async () => {
  const { spawnNative } = await import("../launcher/lib/launcher.js");
  const fixtureRoot = await mkdtemp(path.join(os.tmpdir(), "ai-cli-gateway-spawn-"));
  temporaryRoots.push(fixtureRoot);
  const beforeSigint = process.listenerCount("SIGINT");
  const beforeSigterm = process.listenerCount("SIGTERM");

  await assert.rejects(spawnNative(path.join(fixtureRoot, "missing"), []), {
    name: "LauncherError",
    code: "SPAWN_FAILED",
    message: "ai-cli-gateway: native executable could not be started",
  });
  assert.equal(process.listenerCount("SIGINT"), beforeSigint);
  assert.equal(process.listenerCount("SIGTERM"), beforeSigterm);
});

test("removes signal listeners after a normal native exit", async () => {
  const { spawnNative } = await import("../launcher/lib/launcher.js");
  const beforeSigint = process.listenerCount("SIGINT");
  const beforeSigterm = process.listenerCount("SIGTERM");

  assert.deepEqual(await spawnNative(process.execPath, ["-e", ""]), {
    code: 0,
    signal: null,
  });
  assert.equal(process.listenerCount("SIGINT"), beforeSigint);
  assert.equal(process.listenerCount("SIGTERM"), beforeSigterm);
});

for (const forwardedSignal of ["SIGINT", "SIGTERM"]) {
  test(`forwards ${forwardedSignal} and preserves signal termination`, async (t) => {
    if (process.platform === "win32") {
      t.skip("Windows does not preserve POSIX signal exits");
      return;
    }
    const fixture = await runnableFixture(`
      const signal = process.argv[2];
      process.once(signal, () => {
        process.stdout.write("observed:" + signal + "\\n", () => {
          process.removeAllListeners(signal);
          process.kill(process.pid, signal);
        });
      });
      process.stdout.write("ready:" + process.pid + "\\n");
      setTimeout(() => process.exit(97), 7_000);
    `);
    const wrapper = runInstalledAsync(fixture, [forwardedSignal]);
    wrapper.stdout.setEncoding("utf8");
    wrapper.stderr.setEncoding("utf8");
    let stdout = "";
    let stderr = "";
    wrapper.stdout.on("data", (chunk) => {
      stdout += chunk;
    });
    wrapper.stderr.on("data", (chunk) => {
      stderr += chunk;
    });

    await waitForMarker(wrapper, () => stdout, "ready:");
    const closed = waitForClose(wrapper);
    process.kill(wrapper.pid, forwardedSignal);
    const result = await closed;

    assert.equal(result.code, null);
    assert.equal(result.signal, forwardedSignal);
    assert.match(stdout, new RegExp(`observed:${forwardedSignal}\\n`));
    assert.equal(stderr, "");
  });
}
