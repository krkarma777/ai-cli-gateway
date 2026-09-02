export const PACKAGE_VERSION = "0.2.1";
export const LAUNCHER_NAME = "ai-cli-gateway";
export const NODE_RANGE = ">=22.14.0";
export const TARGETS = Object.freeze([
  Object.freeze({ key: "darwin-x64", packageName: "@krkarma777/ai-cli-gateway-darwin-x64", platform: "darwin", arch: "x64", goos: "darwin", goarch: "amd64", stagingDirectory: "darwin_amd64", executable: "ai-cli-gateway" }),
  Object.freeze({ key: "darwin-arm64", packageName: "@krkarma777/ai-cli-gateway-darwin-arm64", platform: "darwin", arch: "arm64", goos: "darwin", goarch: "arm64", stagingDirectory: "darwin_arm64", executable: "ai-cli-gateway" }),
  Object.freeze({ key: "linux-x64", packageName: "@krkarma777/ai-cli-gateway-linux-x64", platform: "linux", arch: "x64", goos: "linux", goarch: "amd64", stagingDirectory: "linux_amd64", executable: "ai-cli-gateway" }),
  Object.freeze({ key: "linux-arm64", packageName: "@krkarma777/ai-cli-gateway-linux-arm64", platform: "linux", arch: "arm64", goos: "linux", goarch: "arm64", stagingDirectory: "linux_arm64", executable: "ai-cli-gateway" }),
  Object.freeze({ key: "win32-x64", packageName: "@krkarma777/ai-cli-gateway-win32-x64", platform: "win32", arch: "x64", goos: "windows", goarch: "amd64", stagingDirectory: "windows_amd64", executable: "ai-cli-gateway.exe" }),
]);

export function targetFor(platform, arch) {
  return TARGETS.find((target) => target.platform === platform && target.arch === arch);
}
