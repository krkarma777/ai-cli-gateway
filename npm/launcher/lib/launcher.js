import { spawn } from "node:child_process";
import { lstat, readFile, realpath, stat } from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const MESSAGES = Object.freeze({
  INVALID_LAUNCHER: "ai-cli-gateway: launcher installation is invalid",
  INVALID_NATIVE: "ai-cli-gateway: native package installation is invalid",
  SPAWN_FAILED: "ai-cli-gateway: native executable could not be started",
});

const TARGETS = Object.freeze([
  Object.freeze({
    key: "darwin-x64",
    packageName: "@krkarma777/ai-cli-gateway-darwin-x64",
    platform: "darwin",
    arch: "x64",
    executable: "ai-cli-gateway",
  }),
  Object.freeze({
    key: "darwin-arm64",
    packageName: "@krkarma777/ai-cli-gateway-darwin-arm64",
    platform: "darwin",
    arch: "arm64",
    executable: "ai-cli-gateway",
  }),
  Object.freeze({
    key: "linux-x64",
    packageName: "@krkarma777/ai-cli-gateway-linux-x64",
    platform: "linux",
    arch: "x64",
    executable: "ai-cli-gateway",
  }),
  Object.freeze({
    key: "linux-arm64",
    packageName: "@krkarma777/ai-cli-gateway-linux-arm64",
    platform: "linux",
    arch: "arm64",
    executable: "ai-cli-gateway",
  }),
  Object.freeze({
    key: "win32-x64",
    packageName: "@krkarma777/ai-cli-gateway-win32-x64",
    platform: "win32",
    arch: "x64",
    executable: "ai-cli-gateway.exe",
  }),
]);

const SUPPORTED_TARGETS = TARGETS.map((target) => target.key).join(", ");
const VERSION_PATTERN = /^(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$/u;
const RUNTIME_VALUE_PATTERN = /^[a-z0-9]{1,16}$/u;
const DEFAULT_LAUNCHER_ROOT = path.dirname(
  fileURLToPath(new URL("../package.json", import.meta.url)),
);

export class LauncherError extends Error {
  constructor(code, message) {
    super(message);
    this.name = "LauncherError";
    this.code = code;
  }
}

export function targetFor(platform, arch) {
  return TARGETS.find((target) => target.platform === platform && target.arch === arch);
}

function launcherError(code) {
  return new LauncherError(code, MESSAGES[code]);
}

function sanitizeRuntimeValue(value) {
  return typeof value === "string" && RUNTIME_VALUE_PATTERN.test(value) ? value : "unknown";
}

async function launcherManifest(launcherRoot) {
  try {
    const value = JSON.parse(
      await readFile(path.join(launcherRoot, "package.json"), "utf8"),
    );
    if (
      value === null ||
      typeof value !== "object" ||
      value.name !== "ai-cli-gateway" ||
      typeof value.version !== "string" ||
      !VERSION_PATTERN.test(value.version)
    ) {
      throw launcherError("INVALID_LAUNCHER");
    }
    return value;
  } catch {
    throw launcherError("INVALID_LAUNCHER");
  }
}

function unsupportedPlatform(platform, arch) {
  const sanitizedPlatform = sanitizeRuntimeValue(platform);
  const sanitizedArch = sanitizeRuntimeValue(arch);
  return new LauncherError(
    "UNSUPPORTED_PLATFORM",
    `ai-cli-gateway: unsupported platform "${sanitizedPlatform}-${sanitizedArch}"; supported: ${SUPPORTED_TARGETS}`,
  );
}

function missingNative(target, launcherVersion) {
  return new LauncherError(
    "MISSING_NATIVE",
    `ai-cli-gateway: native package ${target.packageName}@${launcherVersion} is missing; reinstall with "npm install --global ai-cli-gateway@${launcherVersion}" without --omit=optional`,
  );
}

function isExpectedModuleNotFound(error, request) {
  return (
    error !== null &&
    typeof error === "object" &&
    error.code === "MODULE_NOT_FOUND" &&
    typeof error.message === "string" &&
    error.message.split(/\r?\n/u, 1)[0] === `Cannot find module '${request}'`
  );
}

function sameRegularFile(left, right) {
  return (
    left.isFile() &&
    right.isFile() &&
    left.dev === right.dev &&
    left.ino === right.ino &&
    left.size === right.size
  );
}

async function validatedNative(manifestPath, target, launcherVersion) {
  const nativeRoot = path.dirname(manifestPath);
  const binary = path.join(nativeRoot, "bin", target.executable);
  const nativeManifest = JSON.parse(await readFile(manifestPath, "utf8"));

  if (
    nativeManifest === null ||
    typeof nativeManifest !== "object" ||
    nativeManifest.name !== target.packageName ||
    nativeManifest.version !== launcherVersion
  ) {
    throw launcherError("INVALID_NATIVE");
  }

  const before = await lstat(binary, { bigint: true });
  if (before.isSymbolicLink() || !before.isFile()) {
    throw launcherError("INVALID_NATIVE");
  }

  const [realNativeRoot, realBinary] = await Promise.all([
    realpath(nativeRoot),
    realpath(binary),
  ]);
  if (realBinary !== path.join(realNativeRoot, "bin", target.executable)) {
    throw launcherError("INVALID_NATIVE");
  }

  const [actual, after] = await Promise.all([
    stat(realBinary, { bigint: true }),
    lstat(binary, { bigint: true }),
  ]);
  if (
    after.isSymbolicLink() ||
    !sameRegularFile(before, actual) ||
    !sameRegularFile(before, after) ||
    (process.platform !== "win32" && (after.mode & 0o111n) === 0n)
  ) {
    throw launcherError("INVALID_NATIVE");
  }

  return { binary: realBinary, version: launcherVersion };
}

export async function resolveNative({ launcherRoot, platform, arch }) {
  const launcher = await launcherManifest(launcherRoot);
  const sanitizedPlatform = sanitizeRuntimeValue(platform);
  const sanitizedArch = sanitizeRuntimeValue(arch);
  const target = targetFor(sanitizedPlatform, sanitizedArch);
  if (target === undefined) {
    throw unsupportedPlatform(platform, arch);
  }

  const request = `${target.packageName}/package.json`;
  let manifestPath;
  try {
    const require = createRequire(path.join(launcherRoot, "lib", "launcher.js"));
    manifestPath = require.resolve(request);
  } catch (error) {
    if (isExpectedModuleNotFound(error, request)) {
      throw missingNative(target, launcher.version);
    }
    throw launcherError("INVALID_NATIVE");
  }

  try {
    return await validatedNative(manifestPath, target, launcher.version);
  } catch {
    throw launcherError("INVALID_NATIVE");
  }
}

export function spawnNative(binary, args) {
  return new Promise((resolve, reject) => {
    let child;
    try {
      child = spawn(binary, args, {
        shell: false,
        stdio: "inherit",
        windowsHide: false,
      });
    } catch {
      reject(launcherError("SPAWN_FAILED"));
      return;
    }

    const forwardSigint = () => {
      child.kill("SIGINT");
    };
    const forwardSigterm = () => {
      child.kill("SIGTERM");
    };
    const cleanup = () => {
      process.removeListener("SIGINT", forwardSigint);
      process.removeListener("SIGTERM", forwardSigterm);
    };

    process.once("SIGINT", forwardSigint);
    process.once("SIGTERM", forwardSigterm);
    child.once("error", () => {
      cleanup();
      reject(launcherError("SPAWN_FAILED"));
    });
    child.once("exit", (code, signal) => {
      cleanup();
      resolve({ code, signal });
    });
  });
}

export async function run(
  argv,
  {
    launcherRoot = DEFAULT_LAUNCHER_ROOT,
    platform = process.platform,
    arch = process.arch,
  } = {},
) {
  try {
    const { binary } = await resolveNative({ launcherRoot, platform, arch });
    const { code, signal } = await spawnNative(binary, argv);
    if (typeof code === "number") {
      return code;
    }
    if (signal !== null) {
      process.kill(process.pid, signal);
    }
    return 1;
  } catch (error) {
    if (!(error instanceof LauncherError)) {
      throw error;
    }
    process.stderr.write(`${error.message}\n`);
    return 1;
  }
}

export async function main(argv) {
  process.exitCode = await run(argv);
}
