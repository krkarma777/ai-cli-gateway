# npm Package Discoverability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish a complete, searchable `0.2.2` npm cohort whose main package explains the AI MVP use case and whose real Windows x86-64 native binary works through npm's Node, `.cmd`, and PowerShell launch paths.

**Architecture:** Keep one dependency-free Node launcher with five exact native `optionalDependencies`; do not change the Go runtime or download binaries during installation. Put canonical npm copy in a focused repository-only module, verify all static manifests and READMEs against it, extend the existing Windows host-install job instead of adding another CI job, and move the next npm release to tag-only trusted publishing after recovering the original `0.2.1` cohort. Prove the first token-free OIDC cohort before retiring the unused bootstrap credential.

**Tech Stack:** Go 1.26.5, Node.js 22.14.0 and 24.13.0, npm 11.19.1 for trusted-publisher operations, actionlint 1.7.12, ECMAScript modules, `node:test`, PowerShell 7, GitHub Actions, npm trusted publishing, npm provenance.

## Global Constraints

- Launcher description: `Build AI MVPs with Codex CLI, Claude Code, and Gemini CLI through a local OpenAI Responses-compatible API.`
- Launcher keywords, in order: `ai`, `ai-cli`, `ai-gateway`, `llm-gateway`, `openai`, `openai-compatible`, `responses-api`, `codex-cli`, `claude-code`, `gemini-cli`, `local-ai`, `ai-mvp`, `structured-output`, `json-schema`.
- The public launcher remains `ai-cli-gateway`; native packages remain the five existing unscoped names.
- Supported native targets remain exactly `darwin-x64`, `darwin-arm64`, `linux-x64`, `linux-arm64`, and `win32-x64`.
- Windows ARM64 is unsupported; Windows x86-64 must resolve `ai-cli-gateway-win32-x64/bin/ai-cli-gateway.exe`.
- Public package manifests contain no `scripts`; install and first execution perform no application-owned download.
- Launcher native dependencies remain exact pins equal to the launcher version.
- Native packages publish first in canonical order; the launcher publishes last.
- Do not overwrite, regenerate, unpublish, or change any published `0.2.1` name/version pair.
- Do not merge the `0.2.2` workflow conversion until the current main-branch recovery workflow has completed the missing `0.2.1` Windows and launcher publications.
- The release workflow's API dispatch creates an independent `npm-release.yml` run, not a reusable `workflow_call`; configure that exact filename as the npm trusted publisher and keep OIDC authority only on its publish job.
- Keep the bootstrap secret and token unused but recoverable until all six `0.2.2` packages have actually published through OIDC; delete and revoke them immediately after that proof.
- Add no CI job and no new workflow. Windows shim checks run inside the existing `npm-host-install` matrix.
- Run focused tests after each task and the expensive full release gate only once before merge.
- Use `/private/tmp` for writable Go and npm caches; do not use the user's root-owned default npm cache.

## File Structure

### Create

- `npm/scripts/package-copy.js` — canonical launcher description, keywords, platform labels, and exact npm README renderers used by package verification.
- `npm/scripts/verify-windows-launcher.ps1` — native Windows npm shim probe for `.cmd`, PowerShell, stdout, stderr, and exit-code propagation.
- `docs/releases/v0.2.2.md` — user-facing metadata, Windows launcher, upgrade, and integrity release notes.

### Modify

- `npm/launcher/package.json` — searchable launcher metadata and `0.2.2` exact native pins.
- `npm/launcher/README.md` — npm-first product explanation and first-request path.
- `npm/platforms/darwin-x64/package.json`
- `npm/platforms/darwin-arm64/package.json`
- `npm/platforms/linux-x64/package.json`
- `npm/platforms/linux-arm64/package.json`
- `npm/platforms/win32-x64/package.json` — internal-package descriptions and minimal platform keywords.
- `npm/platforms/darwin-x64/README.md`
- `npm/platforms/darwin-arm64/README.md`
- `npm/platforms/linux-x64/README.md`
- `npm/platforms/linux-arm64/README.md`
- `npm/platforms/win32-x64/README.md` — direct users to the launcher.
- `npm/scripts/verify-packages.js` — consume canonical copy and require exact staged and packed metadata.
- `npm/scripts/package-config.js` — advance the six-package cohort to `0.2.2`.
- `npm/test/package-contract.test.js` — independent exact copy and README contracts.
- `npm/test/launcher.test.js` — advance the launcher fixture contract to `0.2.2`.
- `.github/workflows/ci.yml` — keep the same matrix and add the Windows-native shim step; then advance it to `v0.2.2`.
- `.github/workflows/npm-release.yml` — advance to `v0.2.2`, remove the one-time main recovery and bootstrap token, and publish only from the exact tag with OIDC.
- `internal/securitytest/repository_test.go` — close metadata, Windows shim, version, tag-only workflow, and token-free trusted-publishing contracts.
- `README.md`
- `docs/getting-started.md`
- `docs/reference.md` — make `v0.2.2` current while preserving historical `v0.2.1` notes.

### Leave Unchanged

- `npm/launcher/lib/launcher.js` — it already selects `win32-x64`, resolves `.exe`, passes arguments and streams, and preserves numeric exits.
- `npm/launcher/bin/ai-cli-gateway.js` — the public command remains a dependency-free delegation entry.
- `docs/releases/v0.2.1.md` — immutable historical release documentation.
- `.github/workflows/release.yml` — it already dispatches an independent npm workflow run at the immutable release tag and requires no extra OIDC permission.
- All Go runtime packages — this change adds no gateway behavior.

---

## Operational Gate A: Submit the npm False-Positive Request

This gate can run in parallel with Tasks 1–5, but it must finish before the implementation branch merges.

- [ ] **Step 1: Open the authenticated npm support form**

Open `https://www.npmjs.com/support`, sign in if required, and choose the package publication or registry restriction category. Do not use a security-abuse report because this is a false-positive publication block.

- [ ] **Step 2: Submit the exact support request**

Subject:

```text
False-positive spam detection blocks legitimate package publication
```

Body:

```text
Publishing ai-cli-gateway-win32-x64@0.2.1 from GitHub Actions fails with:

E403 Package name triggered spam detection.

This is a legitimate open-source platform package for AI CLI Gateway.

Repository:
https://github.com/krkarma777/ai-cli-gateway

Immutable release:
https://github.com/krkarma777/ai-cli-gateway/releases/tag/v0.2.1

Provenance workflow run:
https://github.com/krkarma777/ai-cli-gateway/actions/runs/33458990165

Four sibling platform packages at 0.2.1 were accepted with npm provenance:
- ai-cli-gateway-darwin-x64
- ai-cli-gateway-darwin-arm64
- ai-cli-gateway-linux-x64
- ai-cli-gateway-linux-arm64

Please review and clear the false-positive name restriction for:
- ai-cli-gateway-win32-x64
- ai-cli-gateway

The second name is the main launcher. It has not yet been attempted because
the publication workflow intentionally stops before publishing the launcher
when any exact native dependency is unavailable.
```

Expected: npm displays a ticket or confirmation. Complete any CAPTCHA and final submit manually. Include no token, `.npmrc`, secret, tarball, or authentication output.

- [ ] **Step 3: Record only the non-secret ticket identifier**

Keep the ticket identifier in the task handoff, not in the repository. Expected state: support is pending or has cleared both package names.

---

### Task 1: Add Exact Search Metadata and npm README Contracts

**Files:**

- Create: `npm/scripts/package-copy.js`
- Modify: `npm/test/package-contract.test.js`
- Modify: `npm/launcher/package.json`
- Modify: `npm/launcher/README.md`
- Modify: `npm/platforms/darwin-x64/package.json`
- Modify: `npm/platforms/darwin-arm64/package.json`
- Modify: `npm/platforms/linux-x64/package.json`
- Modify: `npm/platforms/linux-arm64/package.json`
- Modify: `npm/platforms/win32-x64/package.json`
- Modify: `npm/platforms/darwin-x64/README.md`
- Modify: `npm/platforms/darwin-arm64/README.md`
- Modify: `npm/platforms/linux-x64/README.md`
- Modify: `npm/platforms/linux-arm64/README.md`
- Modify: `npm/platforms/win32-x64/README.md`
- Modify: `npm/scripts/verify-packages.js`

**Interfaces:**

- Produces: `LAUNCHER_DESCRIPTION: string`, `LAUNCHER_KEYWORDS: readonly string[]`, `nativeDescription(target): string`, `nativeKeywords(target): string[]`, `launcherReadme(nodeRange): string`, and `nativeReadme(target): string`.
- Consumes: target objects from `npm/scripts/package-config.js` with `key`, `packageName`, `platform`, `arch`, `goos`, and `goarch`.

- [ ] **Step 1: Write failing independent metadata tests**

Add these constants and helper to `npm/test/package-contract.test.js`; do not import expected copy from `package-copy.js`, because the tests must remain independent:

```js
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

async function packageText(relative) {
  return readFile(path.join(npmRoot, relative), "utf8");
}
```

Add three contracts:

```js
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
    "npm provenance",
  ]) {
    assert.ok(readme.includes(required), `launcher README missing ${required}`);
  }
});

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
```

- [ ] **Step 2: Run the focused test and confirm RED**

Run:

```bash
node --test --test-concurrency=1 npm/test/package-contract.test.js
```

Expected: FAIL because the launcher has the old description and no keywords, and the current READMEs lack the required product copy.

- [ ] **Step 3: Create the canonical repository-only package copy module**

Create `npm/scripts/package-copy.js` with the exact constants above and this platform map:

```js
export const LAUNCHER_DESCRIPTION =
  "Build AI MVPs with Codex CLI, Claude Code, and Gemini CLI through a local OpenAI Responses-compatible API.";

export const LAUNCHER_KEYWORDS = Object.freeze([
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
]);

const PLATFORM_LABELS = Object.freeze({
  "darwin-x64": "macOS Intel",
  "darwin-arm64": "macOS Apple silicon",
  "linux-x64": "Linux x86-64",
  "linux-arm64": "Linux ARM64",
  "win32-x64": "Windows x86-64",
});

function platformLabel(target) {
  const label = PLATFORM_LABELS[target.key];
  if (label === undefined) {
    throw new Error("unknown npm package target");
  }
  return label;
}

export function nativeDescription(target) {
  return `Internal ${platformLabel(target)} binary for AI CLI Gateway. Install the ai-cli-gateway package instead.`;
}

export function nativeKeywords(target) {
  return ["ai-cli-gateway", "native-binary", target.platform, target.arch];
}
```

Add `launcherReadme(nodeRange)` and this exact `nativeReadme(target)` export. `launcherReadme` must render exactly the checked-in launcher README described in Step 4.

```js
export function nativeReadme(target) {
  const label = platformLabel(target);
  return `# ${target.packageName}

> Internal platform package for ${label}. Install \`ai-cli-gateway\` instead.

\`\`\`console
npm install --global ai-cli-gateway
\`\`\`

Target: \`${target.key}\` (\`npm os=${target.platform}\`, \`npm cpu=${target.arch}\`, \`GOOS=${target.goos}\`, \`GOARCH=${target.goarch}\`).

This native binary is installed automatically through an exact optional dependency of the main launcher. Do not install or invoke this package directly.

No standalone JavaScript API is provided.

- [Main npm package](https://www.npmjs.com/package/ai-cli-gateway)
- [GitHub repository](https://github.com/krkarma777/ai-cli-gateway)
`;
}
```

The renderer interpolates only fields from the canonical target object and ends every generated README with one newline.

- [ ] **Step 4: Replace the launcher npm README with the exact npm-first structure**

Write `npm/launcher/README.md` with these sections and no version-specific number:

````md
# AI CLI Gateway

Build AI MVPs with Codex CLI, Claude Code, and Gemini CLI through a local OpenAI Responses-compatible API.

AI CLI Gateway turns locally authenticated AI CLIs into a focused Responses API-compatible subset. Your application calls one loopback endpoint; the gateway runs the configured CLI and returns final text or locally validated JSON.

## Install

```console
npm install --global ai-cli-gateway
ai-cli-gateway version
```

Node.js `>=22.14.0` is required.

## Quick start

1. Install and authenticate Codex CLI, Claude Code, or Gemini CLI with the provider's own tooling.
2. Run `ai-cli-gateway init` and configure at least one model alias.
3. Run `ai-cli-gateway serve`.

The listener defaults to `http://127.0.0.1:8080`.

## Connect with the OpenAI JavaScript SDK

```js
import OpenAI from "openai";

const client = new OpenAI({
  apiKey: process.env.AI_CLI_GATEWAY_API_KEY,
  baseURL: "http://127.0.0.1:8080/v1",
  timeout: 300_000,
  maxRetries: 0,
});

const response = await client.responses.create({
  model: "YOUR_ALIAS",
  input: "Propose three names for my AI MVP.",
  stream: false,
  store: false,
  tools: [],
  tool_choice: "none",
});

console.log(response.output_text);
```

## What it supports

- Codex CLI, Claude Code, and Gemini CLI
- macOS Intel and Apple silicon, Linux x86-64 and ARM64, and Windows x86-64
- `POST /v1/responses` and `GET /v1/models`
- final non-streaming text
- strict JSON Schema structured output validated locally
- operator-configured model aliases
- guided init, Doctor diagnostics, bounded queues, timeouts, and process cleanup

It is useful for AI MVPs, product validation, demos, hackathons, structured-output prototypes, and local SDK integrations.

## Focused compatibility

This is not the full OpenAI API. It does not support SSE streaming, tool-call round trips, stored responses, gateway sessions, conversation history, multimodal input, background execution, or other OpenAI endpoints.

## Security and distribution

Provider credentials remain owned by provider tooling. The gateway listens on loopback, avoids shell interpolation, and does not log prompts, model output, or credentials.

The launcher installs one host-specific optional dependency. Public packages define no lifecycle scripts, perform no application-owned binary download, and carry npm provenance.

## Documentation

- [Getting Started](https://github.com/krkarma777/ai-cli-gateway/blob/main/docs/getting-started.md)
- [API and Operations Reference](https://github.com/krkarma777/ai-cli-gateway/blob/main/docs/reference.md)
- [Security Policy](https://github.com/krkarma777/ai-cli-gateway/blob/main/SECURITY.md)
- [GitHub Releases](https://github.com/krkarma777/ai-cli-gateway/releases)
- [GitHub repository](https://github.com/krkarma777/ai-cli-gateway)
````

- [ ] **Step 5: Update all six manifests and five native READMEs**

Add the exact launcher `description` and `keywords` from Global Constraints to `npm/launcher/package.json`. For each native manifest, set the exact description and keyword array from the Step 1 `platformCopy` map. Preserve every other manifest field and field order.

Render the five native READMEs from `nativeReadme(target)` and commit their static output. Do not add scripts, dependencies, a `bin` field, or a JavaScript export to a native package.

- [ ] **Step 6: Make staged-package verification consume the canonical copy**

In `npm/scripts/verify-packages.js`, import:

```js
import {
  LAUNCHER_DESCRIPTION,
  LAUNCHER_KEYWORDS,
  launcherReadme,
  nativeDescription,
  nativeKeywords,
  nativeReadme,
} from "./package-copy.js";
```

Change `expectedLauncherManifest(version)` to use:

```js
description: LAUNCHER_DESCRIPTION,
keywords: [...LAUNCHER_KEYWORDS],
```

Change `expectedNativeManifest(target, version)` to use:

```js
description: nativeDescription(target),
keywords: nativeKeywords(target),
```

Remove the old local `launcherReadme` and `nativeReadme` functions. Continue comparing static source, staged, and packed README bytes to the canonical renderer.

- [ ] **Step 7: Run focused and complete npm verification**

Run:

```bash
node --test --test-concurrency=1 npm/test/package-contract.test.js
node npm/scripts/verify-packages.js \
  --source-check \
  --repository-root "$PWD" \
  --version 0.2.1
npm test --prefix npm
git diff --check
```

Expected: all metadata, README, source-check, and npm tests pass; no lifecycle script appears.

- [ ] **Step 8: Commit**

```bash
git add \
  npm/launcher \
  npm/platforms \
  npm/scripts/package-copy.js \
  npm/scripts/verify-packages.js \
  npm/test/package-contract.test.js
git commit -m "feat: improve npm package discoverability"
```

---

### Task 2: Prove the Real Windows npm Launcher Path

**Files:**

- Create: `npm/scripts/verify-windows-launcher.ps1`
- Modify: `.github/workflows/ci.yml`
- Modify: `internal/securitytest/repository_test.go`

**Interfaces:**

- Consumes: the installed prefix `${RUNNER_TEMP}/npm-host-install/install`, `EXPECTED_TAG`, and `GITHUB_SHA`.
- Produces: a zero exit only after both npm-generated Windows shims reach the matching native `.exe`; invalid-command exit must remain `2`.

- [ ] **Step 1: Write the failing closed-workflow contract**

Extend `expectedCIContracts()["npm-host-install"].steps` with a new `npmWindowsLauncherStepContract()` after `npmHostInstallStepContract()`. The exact step contract is:

```yaml
- name: Verify native Windows npm shims
  if: runner.os == 'Windows'
  shell: pwsh
  env:
    EXPECTED_TAG: v0.2.1
  run: |
    $ErrorActionPreference = "Stop"
    $InstallPrefix = [IO.Path]::GetFullPath(
      (Join-Path $env:RUNNER_TEMP "npm-host-install/install")
    )
    ./npm/scripts/verify-windows-launcher.ps1 `
      -InstallPrefix $InstallPrefix `
      -ExpectedTag $env:EXPECTED_TAG `
      -ExpectedCommit $env:GITHUB_SHA
```

Add mutation cases that remove the step, widen its `if`, rename the script, remove `.cmd` verification, remove `.ps1` verification, or change the expected invalid-command exit from `2` to `0`. Each mutation must be rejected by `validateSDKCIWorkflowContract`.

- [ ] **Step 2: Run the focused workflow tests and confirm RED**

Run with writable Go caches:

```bash
task_verify_root="$(mktemp -d /private/tmp/ai-cli-gateway-windows-contract.XXXXXX)"
GOCACHE="${task_verify_root}/gocache" \
GOMODCACHE="${task_verify_root}/gomodcache" \
GOPATH="${task_verify_root}/gopath" \
go test -count=1 ./internal/securitytest \
  -run '^(TestWorkflowMultiPlatformReleaseContract|TestWorkflowMultiPlatformReleaseContractRejectsMutations)$'
```

Expected: FAIL because the Windows shim step and PowerShell verifier do not exist.

- [ ] **Step 3: Implement the PowerShell verifier**

Create `npm/scripts/verify-windows-launcher.ps1` with mandatory validated parameters:

```powershell
[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [string]$InstallPrefix,

  [Parameter(Mandatory = $true)]
  [ValidatePattern('^v(?:0|[1-9][0-9]*)[.](?:0|[1-9][0-9]*)[.](?:0|[1-9][0-9]*)$')]
  [string]$ExpectedTag,

  [Parameter(Mandatory = $true)]
  [ValidatePattern('^[0-9a-f]{40}$')]
  [string]$ExpectedCommit
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$ResolvedPrefix = [IO.Path]::GetFullPath($InstallPrefix)
if (-not [IO.Path]::IsPathFullyQualified($InstallPrefix) -or
    $ResolvedPrefix -cne $InstallPrefix -or
    -not (Test-Path -LiteralPath $ResolvedPrefix -PathType Container)) {
  throw 'windows launcher verification failed'
}

$CmdShim = Join-Path $ResolvedPrefix 'ai-cli-gateway.cmd'
$PowerShellShim = Join-Path $ResolvedPrefix 'ai-cli-gateway.ps1'
$CmdPath = [IO.Path]::GetFullPath($env:ComSpec)
$PowerShellPath = [IO.Path]::GetFullPath((Get-Process -Id $PID).Path)
foreach ($Shim in @($CmdShim, $PowerShellShim)) {
  $Item = Get-Item -LiteralPath $Shim -Force
  if ($Item.PSIsContainer -or
      -not [string]::IsNullOrEmpty([string]$Item.LinkType)) {
    throw 'windows launcher verification failed'
  }
}

function Invoke-Shim(
  [ValidateSet('cmd', 'powershell')]
  [string]$Kind,
  [string]$Shim,
  [string]$Argument
) {
  $StartInfo = [Diagnostics.ProcessStartInfo]::new()
  $StartInfo.UseShellExecute = $false
  $StartInfo.CreateNoWindow = $true
  $StartInfo.RedirectStandardOutput = $true
  $StartInfo.RedirectStandardError = $true
  if ($Kind -ceq 'cmd') {
    $StartInfo.FileName = $CmdPath
    foreach ($Value in @('/d', '/s', '/c', 'call', $Shim, $Argument)) {
      $StartInfo.ArgumentList.Add($Value)
    }
  } else {
    $StartInfo.FileName = $PowerShellPath
    foreach ($Value in @(
      '-NoLogo',
      '-NoProfile',
      '-NonInteractive',
      '-File',
      $Shim,
      $Argument
    )) {
      $StartInfo.ArgumentList.Add($Value)
    }
  }

  $Process = [Diagnostics.Process]::new()
  $Process.StartInfo = $StartInfo
  try {
    if (-not $Process.Start()) {
      throw 'windows launcher verification failed'
    }
    $StdoutTask = $Process.StandardOutput.ReadToEndAsync()
    $StderrTask = $Process.StandardError.ReadToEndAsync()
    $Process.WaitForExit()
    $Stdout = $StdoutTask.GetAwaiter().GetResult().Replace("`r`n", "`n")
    $Stderr = $StderrTask.GetAwaiter().GetResult().Replace("`r`n", "`n")
    return [PSCustomObject]@{
      Status = $Process.ExitCode
      Stdout = $Stdout
      Stderr = $Stderr
    }
  } finally {
    $Process.Dispose()
  }
}

function Assert-VersionResult([PSCustomObject]$Result) {
  if ($Result.Status -ne 0 -or $Result.Stderr -cne '') {
    throw 'windows launcher verification failed'
  }
  $Value = $Result.Stdout.TrimEnd("`r", "`n")
  $Pattern = '^ai-cli-gateway ' +
    [Regex]::Escape($ExpectedTag) +
    ' [(]' +
    [Regex]::Escape($ExpectedCommit) +
    ', [0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z[)]$'
  if ($Value -cnotmatch $Pattern) {
    throw 'windows launcher verification failed'
  }
  return $Value
}

$CmdVersion = Assert-VersionResult (Invoke-Shim 'cmd' $CmdShim 'version')
$PowerShellVersion = Assert-VersionResult (
  Invoke-Shim 'powershell' $PowerShellShim 'version'
)
if ($CmdVersion -cne $PowerShellVersion) {
  throw 'windows launcher verification failed'
}

function Assert-InvalidResult([PSCustomObject]$Result) {
  $Usage = "usage:`n" +
    "  ai-cli-gateway version`n" +
    "  ai-cli-gateway init [OPTIONS]`n" +
    "  ai-cli-gateway serve [--config PATH]`n" +
    "  ai-cli-gateway doctor [--config PATH] [--json]`n"
  if ($Result.Status -ne 2 -or
      $Result.Stdout -cne '' -or
      $Result.Stderr -cne $Usage) {
      throw 'windows launcher verification failed'
  }
}

Assert-InvalidResult (Invoke-Shim 'cmd' $CmdShim '__launcher_exit_probe__')
Assert-InvalidResult (
  Invoke-Shim 'powershell' $PowerShellShim '__launcher_exit_probe__'
)
```

Both shims run as child processes. This is required because an npm-generated PowerShell shim uses `exit` and must not be dot-sourced or invoked inside the verifier's own process. The verifier creates and deletes no files.

- [ ] **Step 4: Add the conditional PowerShell step to the existing matrix**

Insert the exact YAML from Step 1 immediately after the existing host package Bash step. Do not add a runner, job, secret, cache, artifact, retry, or `continue-on-error`.

- [ ] **Step 5: Run focused workflow-contract verification**

Run:

```bash
task_verify_root="$(mktemp -d /private/tmp/ai-cli-gateway-windows-green.XXXXXX)"
GOCACHE="${task_verify_root}/gocache" \
GOMODCACHE="${task_verify_root}/gomodcache" \
GOPATH="${task_verify_root}/gopath" \
go test -count=1 ./internal/securitytest \
  -run '^(TestWorkflowMultiPlatformReleaseContract|TestWorkflowMultiPlatformReleaseContractRejectsMutations)$'
git diff --check
```

Expected: the focused YAML/closed-contract Go tests pass. Run pinned actionlint once in Task 5; the real `.cmd`, PowerShell, and native `.exe` execution remains a required `windows-2025` CI result.

- [ ] **Step 6: Commit**

```bash
git add \
  .github/workflows/ci.yml \
  internal/securitytest/repository_test.go \
  npm/scripts/verify-windows-launcher.ps1
git commit -m "test: verify Windows npm launcher shims"
```

---

### Task 3: Advance Public Packages and Current Documentation to `0.2.2`

**Files:**

- Create: `docs/releases/v0.2.2.md`
- Modify: `npm/scripts/package-config.js`
- Modify: all six public package manifests
- Modify: `npm/test/package-contract.test.js`
- Modify: `npm/test/launcher.test.js`
- Modify: `.github/workflows/ci.yml`
- Modify: `README.md`
- Modify: `docs/getting-started.md`
- Modify: `docs/reference.md`
- Modify: `internal/securitytest/repository_test.go`

**Interfaces:**

- Produces: one exact six-package `0.2.2` cohort and current documentation for `v0.2.2`.
- Preserves: historical `docs/releases/v0.2.1.md` and every historical contract fixture intentionally testing `v0.2.1`.

- [ ] **Step 1: Change current-version tests first**

Update the package test constants to `0.2.2`, update current README/reference/getting-started expectations to `v0.2.2`, and add a `docs/releases/v0.2.2.md` contract with these exact headings:

```text
AI CLI Gateway v0.2.2
npm package discovery
Windows npm launcher
Install or update
Compatibility and integrity
```

Require the new notes to contain the approved launcher description, all three provider names, `npm install --global ai-cli-gateway@0.2.2`, all five native targets, the `.cmd` and PowerShell launcher gate, focused Responses compatibility, no lifecycle scripts, provenance, and tag-pinned `v0.2.2` documentation links.

- [ ] **Step 2: Run focused tests and confirm RED**

Run:

```bash
task_verify_root="$(mktemp -d /private/tmp/ai-cli-gateway-version-red.XXXXXX)"
node --test --test-concurrency=1 npm/test/package-contract.test.js
GOCACHE="${task_verify_root}/gocache" \
GOMODCACHE="${task_verify_root}/gomodcache" \
GOPATH="${task_verify_root}/gopath" \
go test -count=1 ./internal/securitytest \
  -run 'ReleaseNotes|CurrentRelease|GettingStarted|WorkflowMultiPlatform'
```

Expected: FAIL because sources, current docs, and CI are still pinned to `0.2.1`.

- [ ] **Step 3: Advance the six package sources together**

Change:

```js
export const PACKAGE_VERSION = "0.2.2";
```

Set every public manifest version to `0.2.2`, and set all five launcher `optionalDependencies` values to exact `0.2.2`. Change `launcherVersion` and `version` test constants to `0.2.2`. Do not modify names, target constraints, engines, files, license, or publish configuration.

- [ ] **Step 4: Advance the existing host-install CI contract**

In `.github/workflows/ci.yml`, change only the current package cohort:

```yaml
TAG=v0.2.2
--version 0.2.2
EXPECTED_TAG: v0.2.2
```

Change the exact version regex to `v0[.]2[.]2`. Mirror each current-value change in `internal/securitytest/repository_test.go`. Preserve matrix runners and the Windows shim step.

- [ ] **Step 5: Make `v0.2.2` current in user documentation**

Update `README.md`, `docs/getting-started.md`, and `docs/reference.md` so install commands, download assets, checksum examples, tag refs, reference titles, and current release links use `0.2.2` or `v0.2.2`.

Create `docs/releases/v0.2.2.md` with the exact headings from Step 1 and these facts:

- npm search metadata now describes AI MVPs, the three supported CLIs, and the local Responses-compatible boundary;
- the launcher README includes install, init, serve, an OpenAI SDK example, compatibility limits, security, and provenance;
- native package pages identify themselves as internal dependencies;
- the real Windows x86-64 `.exe` is installed with the launcher and checked through npm's `.cmd` and PowerShell shims;
- runtime behavior and the focused API subset are unchanged from `v0.2.1`;
- the release retains five native targets, checksums, SBOM, GitHub attestations, exact npm native equivalence, and npm provenance.

Do not edit `docs/releases/v0.2.1.md`.

- [ ] **Step 6: Run source and current-document contracts**

Run:

```bash
task_verify_root="$(mktemp -d /private/tmp/ai-cli-gateway-version-green.XXXXXX)"
node npm/scripts/verify-packages.js \
  --source-check \
  --repository-root "$PWD" \
  --version 0.2.2
node --test --test-concurrency=1 npm/test/package-contract.test.js npm/test/launcher.test.js
GOCACHE="${task_verify_root}/gocache" \
GOMODCACHE="${task_verify_root}/gomodcache" \
GOPATH="${task_verify_root}/gopath" \
go test -count=1 ./internal/securitytest \
  -run 'ReleaseNotes|CurrentRelease|GettingStarted|WorkflowMultiPlatform'
git diff --check
```

Expected: package sources, launcher fixtures, current docs, historical `v0.2.1` docs, and host-install contracts pass.

- [ ] **Step 7: Commit**

```bash
git add \
  .github/workflows/ci.yml \
  README.md \
  docs/getting-started.md \
  docs/reference.md \
  docs/releases/v0.2.2.md \
  internal/securitytest/repository_test.go \
  npm
git commit -m "docs: prepare npm discoverability release v0.2.2"
```

---

### Task 4: Convert `v0.2.2` npm Publication to Tag-Only Trusted Publishing

**Files:**

- Modify: `.github/workflows/npm-release.yml`
- Modify: `internal/securitytest/repository_test.go`

**Interfaces:**

- Consumes: exact immutable `v0.2.2` tag dispatch, six configured npm trusted publishers, GitHub-hosted runner OIDC.
- Produces: token-free `npm publish` with npm 11.19.1 and automatic provenance.

- [ ] **Step 1: Change the closed workflow tests first**

Change the npm release workflow contract to require:

```text
INPUT_TAG is exactly v0.2.2
EVENT_REF is exactly refs/tags/v0.2.2
EVENT_SHA equals the peeled live tag commit
no refs/heads/main recovery path
no source_mode, live_main, or main-ref API query
no NODE_AUTH_TOKEN or NPM_TOKEN
publish job retains contents:read and id-token:write
setup-node remains pinned to 820762786026740c76f36085b0efc47a31fe5020 (v7.0.0)
OIDC claims identify npm-release.yml at refs/tags/v0.2.2 on a GitHub-hosted runner
npm 11.19.1 is installed with lifecycle scripts disabled
all assets, tarballs, package specs, and artifact names are exactly 0.2.2
```

Replace the old recovery and bootstrap-token mutation cases with mutations that add a branch ref, restore a token env, remove `id-token: write`, alter the exact OIDC `workflow_ref`, loosen the exact tag, skip the npm 11.19.1 assertion, or publish the launcher before a native package.

- [ ] **Step 2: Run the npm workflow contracts and confirm RED**

Run:

```bash
task_verify_root="$(mktemp -d /private/tmp/ai-cli-gateway-npm-workflow-red.XXXXXX)"
GOCACHE="${task_verify_root}/gocache" \
GOMODCACHE="${task_verify_root}/gomodcache" \
GOPATH="${task_verify_root}/gopath" \
go test -count=1 ./internal/securitytest \
  -run 'NPMReleaseWorkflow'
```

Expected: FAIL because the workflow still contains the `v0.2.1` recovery branch and bootstrap token.

- [ ] **Step 3: Remove the one-time recovery authority**

In `Validate immutable release metadata`, replace the branch/recovery case and conditional main lookup with:

```bash
test "${INPUT_TAG}" = v0.2.2
test "${EVENT_REF}" = "refs/tags/${INPUT_TAG}"
[[ "${EVENT_SHA}" =~ ${commit_pattern} ]]
tag_commit="$(resolve_live_tag)"
[[ "${tag_commit}" =~ ${commit_pattern} ]]
test "${EVENT_SHA}" = "${tag_commit}"
```

Keep the live release lookup, immutable state, exact seven assets, and outputs. Keep the tag-commit ancestor check against `origin/main` in source validation.

- [ ] **Step 4: Advance every closed release artifact to `0.2.2`**

Change all source version guards, release asset names, archive names, npm tarball names, artifact names, descriptor versions, registry package specs, and integrity rechecks from `0.2.1` to `0.2.2`.

After the edit, this command must print nothing:

```bash
rg -n '0[.]2[.]1|v0[.]2[.]1' .github/workflows/npm-release.yml
```

- [ ] **Step 5: Pin the trusted-publishing npm CLI, prove the OIDC identity, and remove token auth**

Add a publish-job shell step immediately after `actions/setup-node`:

```yaml
- name: Install trusted-publishing npm CLI
  shell: bash
  run: |
    set -euo pipefail
    npm install --global --ignore-scripts --no-audit --no-fund npm@11.19.1
    test "$(npm --version)" = 11.19.1
```

Keep `actions/setup-node@820762786026740c76f36085b0efc47a31fe5020` (v7.0.0), `package-manager-cache: false`, and `registry-url`. Version 7 removed the dummy `NODE_AUTH_TOKEN` fallback that interfered with OIDC in older releases.

Add this step after the npm CLI version assertion. It requests the same npm-registry audience used by npm CLI, decodes only the JWT payload in memory, prints neither the token nor its claims, and proves this API-dispatched run is the trusted workflow:

```yaml
- name: Validate npm trusted-publishing identity
  shell: bash
  env:
    EXPECTED_TAG: v0.2.2
  run: |
    set -euo pipefail
    node --input-type=module <<'NODE'
    const required = (name) => {
      const value = process.env[name];
      if (typeof value !== "string" || value.length === 0) process.exit(1);
      return value;
    };

    const tag = required("EXPECTED_TAG");
    const endpoint = new URL(required("ACTIONS_ID_TOKEN_REQUEST_URL"));
    endpoint.searchParams.set("audience", "npm:registry.npmjs.org");
    const response = await fetch(endpoint, {
      headers: {
        Accept: "application/json",
        Authorization: `Bearer ${required("ACTIONS_ID_TOKEN_REQUEST_TOKEN")}`,
      },
    });
    if (!response.ok) process.exit(1);
    const body = await response.json();
    if (body === null || typeof body !== "object" || typeof body.value !== "string") {
      process.exit(1);
    }
    const parts = body.value.split(".");
    if (parts.length !== 3) process.exit(1);
    const claims = JSON.parse(Buffer.from(parts[1], "base64url").toString("utf8"));
    const expected = {
      aud: "npm:registry.npmjs.org",
      repository: "krkarma777/ai-cli-gateway",
      workflow_ref: `krkarma777/ai-cli-gateway/.github/workflows/npm-release.yml@refs/tags/${tag}`,
      ref: `refs/tags/${tag}`,
      sha: required("GITHUB_SHA"),
      event_name: "workflow_dispatch",
      runner_environment: "github-hosted",
      repository_visibility: "public",
    };
    for (const [name, value] of Object.entries(expected)) {
      if (claims[name] !== value) process.exit(1);
    }
    NODE
```

Because `release.yml` creates a separate workflow run with `gh workflow run`, it is not an OIDC parent or reusable-workflow caller. Leave `.github/workflows/release.yml` unchanged and do not grant its dispatch job `id-token: write`.

Remove:

```yaml
NODE_AUTH_TOKEN: ${{ secrets.NPM_TOKEN }}
```

Keep `NPM_CONFIG_REGISTRY`, `id-token: write`, the GitHub-hosted runner, `--access public`, and `--provenance`. The explicit provenance flag may remain as a closed assertion even though trusted publishing also creates provenance automatically.

- [ ] **Step 6: Run focused workflow tests**

Run:

```bash
task_verify_root="$(mktemp -d /private/tmp/ai-cli-gateway-npm-workflow-green.XXXXXX)"
GOCACHE="${task_verify_root}/gocache" \
GOMODCACHE="${task_verify_root}/gomodcache" \
GOPATH="${task_verify_root}/gopath" \
go test -count=1 ./internal/securitytest \
  -run 'NPMReleaseWorkflow'
git diff --check
```

Expected: the exact tag-only, token-free, identity-checked npm 11.19.1 workflow and its YAML/closed-contract mutations pass. Run pinned actionlint once in Task 5.

- [ ] **Step 7: Commit**

```bash
git add .github/workflows/npm-release.yml internal/securitytest/repository_test.go
git commit -m "ci: publish npm v0.2.2 with trusted identity"
```

---

### Task 5: Perform One Complete Local Verification and Prepare a Draft PR

**Files:**

- Verify all changed files.
- Do not create a tag, release, or npm publication.

**Interfaces:**

- Consumes: Tasks 1–4.
- Produces: a clean, reviewable branch with one full local verification record.

- [ ] **Step 1: Create isolated writable caches**

Run Steps 1–4 in the same shell session so the exported cache paths and private build root remain exact.

```bash
release_verify_root="$(mktemp -d /private/tmp/ai-cli-gateway-v0.2.2.XXXXXX)"
export GOCACHE="${release_verify_root}/gocache"
export GOMODCACHE="${release_verify_root}/gomodcache"
export GOPATH="${release_verify_root}/gopath"
export NPM_CONFIG_CACHE="${release_verify_root}/npm-cache"
mkdir -p \
  "$GOCACHE" \
  "$GOMODCACHE" \
  "$GOPATH" \
  "$NPM_CONFIG_CACHE" \
  "${release_verify_root}/tools"
GOBIN="${release_verify_root}/tools" \
  go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
readonly actionlint_bin="${release_verify_root}/tools/actionlint"
test "$("${actionlint_bin}" -version | sed -n '1p')" = v1.7.12
```

- [ ] **Step 2: Run fast source, npm, and workflow gates**

```bash
go mod verify
test -z "$(gofmt -l .)"
go vet ./...
golangci-lint run ./...
npm ci --ignore-scripts --prefix npm
npm test --prefix npm
node npm/scripts/verify-packages.js \
  --source-check \
  --repository-root "$PWD" \
  --version 0.2.2
"${actionlint_bin}" \
  -config-file /dev/null \
  -shellcheck= \
  -pyflakes= \
  -no-color \
  .github/workflows/ci.yml \
  .github/workflows/release.yml \
  .github/workflows/npm-release.yml
git diff --check
```

Expected: all commands pass; golangci-lint reports `0 issues`.

- [ ] **Step 3: Run the expensive Go gates once**

```bash
go test -count=1 ./...
go test -race -timeout=20m -count=1 ./...
go test -tags=integration -count=1 ./...
go test -trimpath -count=1 ./...
GOFLAGS=-trimpath go test -count=1 ./internal/testutil ./internal/testcli
go test -tags=live -run '^$' ./internal/provider/...
```

Expected: all packages pass. Do not repeat the whole set after documentation-only corrections; rerun only the affected focused gate unless executable source changes.

- [ ] **Step 4: Pack and inspect all six packages from cross-built binaries**

Cross-build all five target binaries with exact `v0.2.2` release metadata:

```bash
release_tag=v0.2.2
release_commit="$(git rev-parse HEAD)"
source_epoch="$(git show -s --format=%ct "$release_commit")"
source_date="$(
  node -e \
    'process.stdout.write(new Date(Number(process.argv[1]) * 1000).toISOString().replace(".000Z", "Z"))' \
    "$source_epoch"
)"
ldflags="-s -w -X github.com/krkarma777/ai-cli-gateway/internal/buildinfo.Version=${release_tag} -X github.com/krkarma777/ai-cli-gateway/internal/buildinfo.Commit=${release_commit} -X github.com/krkarma777/ai-cli-gateway/internal/buildinfo.Date=${source_date}"
for target_record in \
  darwin_amd64:darwin:amd64:ai-cli-gateway \
  darwin_arm64:darwin:arm64:ai-cli-gateway \
  linux_amd64:linux:amd64:ai-cli-gateway \
  linux_arm64:linux:arm64:ai-cli-gateway \
  windows_amd64:windows:amd64:ai-cli-gateway.exe
do
  IFS=: read -r staging_directory target_goos target_goarch target_executable \
    <<<"$target_record"
  mkdir -p "${release_verify_root}/binaries/${staging_directory}"
  CGO_ENABLED=0 GOOS="$target_goos" GOARCH="$target_goarch" \
    go build -trimpath -buildvcs=false -mod=readonly -ldflags "$ldflags" \
      -o "${release_verify_root}/binaries/${staging_directory}/${target_executable}" \
      ./cmd/ai-cli-gateway
done

node npm/scripts/stage-packages.js \
  --repository-root "$PWD" \
  --binary-root "${release_verify_root}/binaries" \
  --output-root "${release_verify_root}/staging" \
  --version 0.2.2
node npm/scripts/verify-packages.js \
  --staging-root "${release_verify_root}/staging" \
  --tarball-root "${release_verify_root}/tarballs" \
  --descriptor "${release_verify_root}/tarballs/packages.json" \
  --version 0.2.2
```

Expected: six tarballs in native-first, launcher-last order with exact descriptions, keywords, READMEs, target constraints, and no lifecycle scripts.

- [ ] **Step 5: Confirm the branch is clean and review the exact diff**

```bash
git status --short --branch
git diff origin/main...HEAD --check
git diff --stat origin/main...HEAD
git log --oneline --decorate origin/main..HEAD
```

Expected: no unstaged files; only the design, plan, metadata, README, Windows gate, release, and contract changes.

- [ ] **Step 6: Push and open a draft PR**

Push without force and open a draft PR titled:

```text
feat: improve npm discovery and Windows launcher verification
```

The PR body must state:

- no runtime Go behavior changed;
- no new CI job was added;
- Windows is verified through the real x86-64 npm package, `.cmd`, and PowerShell shims;
- merge is held until the missing `0.2.1` packages are recovered and trusted publishers are configured;
- full local verification passed once.

---

## Operational Gate B: Complete `0.2.1` and Configure Trusted Publishing

Do this on the current `origin/main` workflow before merging the draft `0.2.2` PR.

- [ ] **Step 1: Wait for npm to clear both package names**

Expected: support explicitly clears `ai-cli-gateway-win32-x64` and confirms `ai-cli-gateway` may be published. Do not retry while the restriction remains.

- [ ] **Step 2: Dispatch the existing recovery workflow**

Record the live main SHA and dispatch:

```bash
git fetch origin main
recovery_sha="$(git rev-parse origin/main)"
test "$recovery_sha" = "9f6200c387c94026d3dc91eccc98ee232b8c9f6c"
gh workflow run npm-release.yml --ref main -f tag=v0.2.1
```

If main has moved, stop and revalidate the new main commit and workflow instead of weakening the equality check.

Expected: the four existing packages are accepted only at exact SRI, Windows publishes next, and the launcher publishes last.

- [ ] **Step 3: Verify all six `0.2.1` packages**

Using a dedicated npm cache, require each package to return version `0.2.1`, one SHA-512 integrity value, registry signatures, and provenance attestations:

```bash
for package_name in \
  ai-cli-gateway-darwin-x64 \
  ai-cli-gateway-darwin-arm64 \
  ai-cli-gateway-linux-x64 \
  ai-cli-gateway-linux-arm64 \
  ai-cli-gateway-win32-x64 \
  ai-cli-gateway
do
  NPM_CONFIG_CACHE=/private/tmp/ai-cli-gateway-npm-cache \
    npm view "${package_name}@0.2.1" \
      name version dist.integrity dist.signatures dist.attestations \
      --json --loglevel=silent
done
```

Compare every SRI to the retained `packages.json` artifact from run `33458990165`. Expected: all six match exactly.

- [ ] **Step 4: Configure six trusted publishers**

On every package's npm settings page configure:

```text
Provider: GitHub Actions
Organization or user: krkarma777
Repository: ai-cli-gateway
Workflow filename: npm-release.yml
Environment: blank
Allowed action: npm publish
```

Account-level 2FA must be enabled. Configure no stage-publish permission.

- [ ] **Step 5: Verify every trust relationship**

With npm 11.19.1 and an authenticated CLI:

```bash
for package_name in \
  ai-cli-gateway-darwin-x64 \
  ai-cli-gateway-darwin-arm64 \
  ai-cli-gateway-linux-x64 \
  ai-cli-gateway-linux-arm64 \
  ai-cli-gateway-win32-x64 \
  ai-cli-gateway
do
  npm trust list "$package_name" --json
done
```

Expected for each: exactly one GitHub relationship for `krkarma777/ai-cli-gateway`, file `npm-release.yml`, publish allowed, no environment, no staged-publish permission.

- [ ] **Step 6: Retain bootstrap authority as an unused rollback until OIDC succeeds**

After all six trust relationships verify:

1. confirm the draft PR workflow contains no `NODE_AUTH_TOKEN` or `NPM_TOKEN`;
2. leave the existing GitHub secret and granular npm token unused;
3. do not yet enable “disallow tokens,” revoke the token, or delete the secret.

Expected: the `v0.2.2` workflow can use only OIDC, while the old credential remains recoverable outside that workflow until the first OIDC publication is proven.

- [ ] **Step 7: Mark the PR ready, require Windows CI, and merge**

Require all branch checks, especially `npm-host-install (windows-2025, win32-x64, windows, amd64, ai-cli-gateway.exe)`. Review the merge tree against the PR tree, then squash-merge. Do not tag from an unverified or moving main branch.

---

### Task 6: Release and Verify `v0.2.2`

**Files:**

- No source edits after the release commit is selected.

**Interfaces:**

- Consumes: merged, CI-green main; all six trusted publishers.
- Produces: immutable GitHub `v0.2.2`, six npm `0.2.2` packages, working Windows launcher, and searchable latest metadata.

- [ ] **Step 1: Record the exact release commit**

Run Steps 1 and 2 in the same clean shell so `release_commit` cannot silently change.

```bash
git fetch --tags origin
release_commit="$(git rev-parse origin/main)"
test -z "$(git status --porcelain)"
test -z "$(git tag --list v0.2.2)"
test -z "$(git ls-remote --tags origin refs/tags/v0.2.2)"
```

Require the main CI run for `release_commit` to be fully successful, including the real Windows npm shim gate.

- [ ] **Step 2: Create and push the annotated tag**

```bash
git tag -a v0.2.2 "$release_commit" -m "AI CLI Gateway v0.2.2"
test "$(git cat-file -t refs/tags/v0.2.2)" = tag
test "$(git rev-parse v0.2.2^{commit})" = "$release_commit"
git push origin refs/tags/v0.2.2
```

Expected: the release workflow creates an immutable release and dispatches the npm workflow at `refs/tags/v0.2.2`.

- [ ] **Step 3: Require both release workflows to pass**

Watch the GitHub Release workflow and npm Release workflow. Do not retry publication blindly. If a package exists, rerun only when registry SRI equals the verified local tarball; if it differs, stop.

Expected: all five native packages publish in canonical order and the launcher publishes last with OIDC and provenance.

- [ ] **Step 4: Verify exact registry metadata and provenance**

For `ai-cli-gateway@0.2.2`, require:

```text
the approved description
the exact ordered keyword array
repository, homepage, bugs, Apache-2.0 license
the full npm README
five exact optionalDependencies at 0.2.2
provenance attestations and registry signatures
```

For every native package, require its exact internal description, four minimal keywords, target README, `os`, `cpu`, SRI, provenance, and signature.

- [ ] **Step 5: Verify a clean install and Windows result**

On a supported host, install from a fresh prefix and dedicated cache with lifecycle scripts disabled, then run:

```bash
npm install --global --ignore-scripts --no-audit --no-fund \
  --prefix /private/tmp/ai-cli-gateway-v0.2.2-install \
  ai-cli-gateway@0.2.2
/private/tmp/ai-cli-gateway-v0.2.2-install/bin/ai-cli-gateway version
```

Expected: exact `v0.2.2` commit and date. On Windows, use the CI result from the immutable tag run to require both `.cmd` and PowerShell shims reached the real `.exe`.

- [ ] **Step 6: Retire bootstrap publication authority after OIDC proof**

Only after Steps 3–5 prove that every package used OIDC with provenance:

1. delete the GitHub Actions secret `NPM_TOKEN`;
2. revoke the granular npm bootstrap token;
3. set every package to require 2FA and disallow traditional token publication;
4. confirm `npm trust list` still reports the one exact `npm-release.yml` publisher for each package;
5. confirm the merged workflow contains no `NODE_AUTH_TOKEN` or `NPM_TOKEN`.

Expected: future publication authority is only the exact GitHub-hosted `npm-release.yml` OIDC identity. If OIDC did not publish all six packages, stop and retain the rollback credential; do not weaken the trusted-publisher relationship.

- [ ] **Step 7: Check exact metadata and npm search indexing without treating delay as a release failure**

With a dedicated cache, inspect the exact package result:

```bash
NPM_CONFIG_CACHE=/private/tmp/ai-cli-gateway-npm-search-cache \
  npm search ai-cli-gateway --json --loglevel=silent
NPM_CONFIG_CACHE=/private/tmp/ai-cli-gateway-npm-search-cache \
  npm search "OpenAI Responses CLI gateway" --json --loglevel=silent
NPM_CONFIG_CACHE=/private/tmp/ai-cli-gateway-npm-search-cache \
  npm search "Codex Claude Gemini CLI API" --json --loglevel=silent
NPM_CONFIG_CACHE=/private/tmp/ai-cli-gateway-npm-search-cache \
  npm search "AI MVP local API" --json --loglevel=silent
NPM_CONFIG_CACHE=/private/tmp/ai-cli-gateway-npm-search-cache \
  npm view ai-cli-gateway@0.2.2 description keywords readme --json --loglevel=silent
```

Expected: `npm view` returns the approved copy immediately. Record a still-pending search index as external npm state and recheck later; do not republish an immutable version to influence indexing.

- [ ] **Step 8: Close release handoff**

Report:

- immutable GitHub release URL;
- npm workflow URL;
- all six exact packages and SRI verification;
- Windows `.cmd` and PowerShell shim result;
- trusted publisher and bootstrap-token removal state;
- current search visibility and next indexing check time.
