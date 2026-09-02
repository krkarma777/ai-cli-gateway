# Getting Started

Install AI CLI Gateway v0.2.1, connect one authenticated provider CLI, and send a first request.

## Install with npm

The npm launcher requires Node.js `>=22.14.0`. The five scoped platform packages are optional internal implementation packages; users install only `ai-cli-gateway`. A normal global installation selects exactly one compatible native package from the five supported targets:

| Host | npm target | Native package |
|---|---|---|
| macOS Intel | `darwin-x64` | `@krkarma777/ai-cli-gateway-darwin-x64` |
| macOS Apple silicon | `darwin-arm64` | `@krkarma777/ai-cli-gateway-darwin-arm64` |
| Linux x86-64 | `linux-x64` | `@krkarma777/ai-cli-gateway-linux-x64` |
| Linux ARM64 | `linux-arm64` | `@krkarma777/ai-cli-gateway-linux-arm64` |
| Windows x86-64 | `win32-x64` | `@krkarma777/ai-cli-gateway-win32-x64` |

Install the exact release and confirm the native CLI version:

```console
npm install --global ai-cli-gateway@0.2.1
ai-cli-gateway version
```

The public packages have no lifecycle scripts. npm resolves the matching optional package from the registry; installation does not download an executable through a lifecycle hook, and the launcher performs no application-owned network download on first execution. The native executable in each npm package is byte-for-byte identical to the executable in its matching GitHub Release archive. The packages carry npm provenance, while the archives, SPDX SBOM, and checksum manifest carry GitHub build-provenance attestations.

`npm audit signatures` verifies downloaded packages' registry signatures and provenance attestations. Run it from a project lockfile that includes the package:

```console
npm audit signatures
```

npm provenance and registry signatures complement the archive verification below; neither replaces checking `SHA256SUMS` and the GitHub attestation when installing an archive manually.

### Update

Update an older installation to this exact release by rerunning the pinned install:

```console
npm install --global ai-cli-gateway@0.2.1
```

### Uninstall

Remove the launcher and its selected native optional package with:

```console
npm uninstall --global ai-cli-gateway
```

### Optional-dependency recovery

If the launcher reports that its native package is missing, remove any `--omit=optional` configuration and reinstall with optional packages enabled:

```console
npm install --global --include=optional ai-cli-gateway@0.2.1
```

The launcher does not search `PATH` for a fallback binary and does not repair the installation by downloading one.

## Quick Start

The normal local path is guided setup followed by serve. Install the binary with the npm path above or use the checksum-verified [v0.2.1 installation procedure](#advanced-recovery-and-service-deployment), stop after placing the binary on `PATH`, and return here. Install and authenticate at least one supported provider CLI with its own tooling before running init.

### Run guided init

```bash
ai-cli-gateway init
```

Select one or more providers, confirm discovered paths, and enter at least one public alias and provider model for each selection. The default file-backed Gateway authentication creates a private `gateway.key` beside the default config. Init shows a redacted semantic diff and a final confirmation before writing, then runs Doctor without inference. A successful ready setup ends with `setup_ready` and prints the resolved paths and next commands.

### Serve

```bash
ai-cli-gateway serve
```

The generated config uses the shared default path, so neither command needs `--config`. Keep this terminal running; the listener defaults to `127.0.0.1:8080`.

### Load the client key safely

In another terminal, load the generated client key into `AI_CLI_GATEWAY_API_KEY` as data. These commands neither print the key nor place the key value in the loader's process arguments.

On POSIX, this derives the same default config base as init, including the absolute-XDG rule:

```bash
set -eu
GATEWAY_CONFIG_BASE="${XDG_CONFIG_HOME:-}"
case "${GATEWAY_CONFIG_BASE}" in
  /*) ;;
  *) GATEWAY_CONFIG_BASE="${HOME:?HOME is required}/.config" ;;
esac
GATEWAY_KEY_FILE="${GATEWAY_CONFIG_BASE}/ai-cli-gateway/gateway.key"
GATEWAY_KEY="$(LC_ALL=C tr -d '\r\n' < "${GATEWAY_KEY_FILE}")"
test "${#GATEWAY_KEY}" -eq 64
case "${GATEWAY_KEY}" in *[!0-9a-f]*) exit 1 ;; esac
export AI_CLI_GATEWAY_API_KEY="${GATEWAY_KEY}"
unset GATEWAY_KEY
```

In PowerShell, use the generated local path under `LOCALAPPDATA`:

```powershell
$GatewayKeyPath = Join-Path $env:LOCALAPPDATA 'AI CLI Gateway\config\gateway.key'
$GatewayKey = [IO.File]::ReadAllText($GatewayKeyPath).TrimEnd("`r", "`n")
if ($GatewayKey -cnotmatch '^[0-9a-f]{64}$') { throw 'invalid gateway key file' }
$env:AI_CLI_GATEWAY_API_KEY = $GatewayKey
Remove-Variable GatewayKey
```

If init prints another key path because `--config` or `--gateway-key-file` was used, substitute that exact printed path. `serve` reads file-backed authentication directly; only the client terminal needs this environment variable.

### Send a request

Save a non-sensitive request as `request.json`. Replace `YOUR_ALIAS` with the exact alias you chose during init:

```json
{
  "model": "YOUR_ALIAS",
  "instructions": "Answer concisely.",
  "input": "Reply with exactly: GATEWAY_OK",
  "text": {
    "format": {
      "type": "text"
    }
  },
  "stream": false,
  "store": false,
  "tools": [],
  "tool_choice": "none"
}
```

Send the file using the configured alias, not the provider model name:

```bash
curl --fail-with-body \
  -H @- \
  -H 'Content-Type: application/json' \
  --data-binary @request.json \
  http://127.0.0.1:8080/v1/responses <<EOF
Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY}
EOF
```

## What guided init asks

The interactive screens follow this sequence:

1. Resolve the default or explicit configuration path and load an existing valid config.
2. **Select providers**: choose one or more of Codex, Claude, and Gemini. The same run supports multiple providers.
3. Confirm each discovered executable and config home, or enter another absolute path. Discovery provides suggestions only; nothing discovered is committed without confirmation.
4. Choose the supported authentication shape for Claude or Gemini. Init records only approved environment names and never asks for credential values; Gemini cached OAuth is not offered.
5. Add one or more alias/provider-model pairs for every selected provider. The same provider can have multiple models, and init never guesses a model or entitlement.
6. Choose Gateway authentication: a generated file, an environment variable name, or none.
7. Review the redacted semantic diff. Each provider or alias replacement is resolved separately as replace or keep existing.
8. Separately confirm reuse of an unreferenced default key or creation of a missing already-configured key when either case applies.
9. Give the final confirmation, the last prompt before any mutation.
10. Read the complete Doctor result and next steps. Init never starts the listener or sends inference.

## Keyboard and accessible prompts

In the visual form, Up/Down moves between options, Space toggles a provider in the multi-select, Enter accepts the current field, and Shift+Tab returns to the previous group. Choose an explicit Back action to revisit provider selection. Ctrl+C cancels; canceling before commit exits 130 without a semantic config change after any auxiliary state is proved restored.

For stable line-oriented prompts, set `AI_CLI_GATEWAY_ACCESSIBLE=1` or use `TERM=dumb` before launching init. Select providers with comma-separated numbers, type `back` to revisit the previous stage, and type `cancel` to stop. Accessible mode still requires both stdin and stderr to be interactive terminals.

If either stdin or stderr is not an interactive terminal, interactive init rejects and exits 2, regardless of whether values were supplied. It writes `init_requires_non_interactive: pass --non-interactive and all required flags` and never waits on a pipe. Automation must explicitly use `--non-interactive` and all required flags.

## Automation with strict flags

`--non-interactive` never reads stdin, runs discovery, searches `PATH`, guesses provider homes, launches login, starts a listener, or performs inference. Values from explicit flags and an existing valid configuration are the only inputs. Every option uses a separate value; equals-sign option syntax is not accepted.

Common and Gateway options:

```text
--config PATH
--non-interactive
--dry-run
--provider codex|claude|gemini              (repeatable)
--replace-provider codex|claude|gemini      (repeatable)
--replace-model ALIAS                       (repeatable)
--gateway-auth file|environment|none
--gateway-key-file ABSOLUTE_PATH
--gateway-key-env ENVIRONMENT_NAME
```

Provider options:

```text
--codex-executable PATH
--codex-entrypoint PATH
--codex-config-home PATH
--codex-model ALIAS=PROVIDER_MODEL           (repeatable)

--claude-executable PATH
--claude-entrypoint PATH
--claude-config-home PATH
--claude-model ALIAS=PROVIDER_MODEL          (repeatable)
--claude-auth config-home|anthropic-api-key

--gemini-executable PATH
--gemini-entrypoint PATH
--gemini-config-home PATH
--gemini-model ALIAS=PROVIDER_MODEL          (repeatable)
--gemini-auth gemini-api-key|google-api-key|vertex-service-account
```

The provider entrypoint flags are only for the supported Windows Node form: the matching executable is an absolute `node.exe` and the entrypoint is one absolute `.js` or `.mjs` file. Native executables and non-Windows configurations do not use an entrypoint.

This example configures multiple providers and multiple models without discovery:

```bash
ai-cli-gateway init \
  --non-interactive \
  --provider codex \
  --provider claude \
  --codex-executable /opt/ai-cli-gateway/bin/codex \
  --codex-config-home /srv/ai-cli-gateway/codex-home \
  --codex-model codex-fast=gpt-fast \
  --codex-model codex-deep=gpt-deep \
  --claude-executable /opt/ai-cli-gateway/bin/claude \
  --claude-config-home /srv/ai-cli-gateway/claude-home \
  --claude-auth config-home \
  --claude-model claude-local=sonnet \
  --gateway-auth file
```

For a new config, `--gateway-auth file` defaults the private key beside the config; `--gateway-key-file` selects another absolute path. `--gateway-auth environment` requires `--gateway-key-env`. `--gateway-auth none` rejects both source flags. Existing Gateway authentication is preserved unless `--gateway-auth` explicitly changes it.

## Merge, dry run, Doctor, and recovery

Init merges into valid TOML: unselected providers, omitted aliases, comments, formatting, server settings, and runtime tuning are preserved. A changed existing provider requires `--replace-provider NAME` in non-interactive mode, and a changed alias requires `--replace-model ALIAS`. Interactive mode asks for each replacement and then shows the converged diff before final confirmation. It never deletes a provider or alias.

`--dry-run` validates input and existing TOML, performs read-only filesystem preflight, and prints the same redacted diff. It creates no directory, key, backup, lock, or temporary file, and explicitly states that no files changed and post-write Doctor was not run.

A successful mutation of an existing config keeps one private `config.toml.bak` containing the immediately prior config; the backup never contains the Gateway key. An identical run is a no-op and does not rewrite the config or backup. After a commit or no-op, Doctor prints all checks, but init readiness depends on core checks and providers selected by this invocation. An unselected existing provider remains visible without changing that result. `setup_saved_but_not_ready` means the validated config was saved but core or a selected provider still needs operator action.

Invalid or unsafe existing files are never repaired automatically. An unapproved orphan key is not silently reused, an invalid key is never overwritten, and a backup restoration that cannot be proved is reported as recovery required. Use the procedure below for explicit configuration, private-directory, environment-key, ACL, and service recovery.

## Advanced recovery and service deployment

This v0.2.1 path installs one release archive without administrator privileges, verifies only the archive that was downloaded, and starts a Codex-backed local gateway with an explicit environment-backed key. Use it when guided init cannot run, when recovering an existing deployment, or when preparing a dedicated service identity.

Choose the one archive matching the machine:

| Host | Release asset |
|---|---|
| Linux x86-64 | `ai-cli-gateway_0.2.1_linux_amd64.tar.gz` |
| Linux ARM64 | `ai-cli-gateway_0.2.1_linux_arm64.tar.gz` |
| macOS Intel | `ai-cli-gateway_0.2.1_darwin_amd64.tar.gz` |
| macOS Apple silicon | `ai-cli-gateway_0.2.1_darwin_arm64.tar.gz` |
| Windows x86-64 | `ai-cli-gateway_0.2.1_windows_amd64.zip` |

The commands below never print or source the gateway key. Install and log in with the official Codex CLI first. The model placeholder is not a provider default or entitlement claim: select a model that the authenticated CLI account can actually access. The gateway never copies Codex authentication files.

### POSIX (macOS and Linux)

Run the following in a Bash or Zsh terminal. It selects one of the four POSIX archives, downloads that archive plus the six-record manifest, selects exactly the archive's one manifest record, and compares its digest before extraction:

```bash
set -eu
VERSION=0.2.1
case "$(uname -s):$(uname -m)" in
  Linux:x86_64) ASSET="ai-cli-gateway_${VERSION}_linux_amd64.tar.gz" ;;
  Linux:aarch64|Linux:arm64) ASSET="ai-cli-gateway_${VERSION}_linux_arm64.tar.gz" ;;
  Darwin:x86_64) ASSET="ai-cli-gateway_${VERSION}_darwin_amd64.tar.gz" ;;
  Darwin:arm64) ASSET="ai-cli-gateway_${VERSION}_darwin_arm64.tar.gz" ;;
  *) printf '%s\n' 'unsupported host tuple' >&2; exit 1 ;;
esac

DOWNLOAD_PARENT="${TMPDIR:-/tmp}"
case "${DOWNLOAD_PARENT}" in /*) ;; *) exit 1 ;; esac
DOWNLOAD_DIR="$(mktemp -d "${DOWNLOAD_PARENT%/}/ai-cli-gateway.XXXXXX")"
chmod 700 "${DOWNLOAD_DIR}"
cd "${DOWNLOAD_DIR}"
RELEASE_URL="https://github.com/krkarma777/ai-cli-gateway/releases/download/v${VERSION}"
curl --disable --fail --show-error --silent --location \
  --output "${ASSET}" "${RELEASE_URL}/${ASSET}"
curl --disable --fail --show-error --silent --location \
  --output SHA256SUMS "${RELEASE_URL}/SHA256SUMS"

MANIFEST_LINE="$(
  awk -v name="${ASSET}" '
    length($0) == 66 + length(name) && substr($0, 65) == " *" name {
      matches++
      selected = $0
    }
    END {
      if (matches != 1) exit 1
      print selected
    }
  ' SHA256SUMS
)" || exit 1
EXPECTED_SHA="${MANIFEST_LINE%% *}"
test "${#EXPECTED_SHA}" -eq 64
case "${EXPECTED_SHA}" in *[!0-9a-f]*) exit 1 ;; esac
ACTUAL_SHA="$(shasum -a 256 "${ASSET}" | awk '{print $1}')"
test "${ACTUAL_SHA}" = "${EXPECTED_SHA}"
```

If the GitHub CLI is installed, optionally verify the canonical build-provenance attestation for that same archive:

```bash
gh attestation verify "${ASSET}" \
  --repo krkarma777/ai-cli-gateway \
  --predicate-type https://slsa.dev/provenance/v1 \
  --signer-workflow github.com/krkarma777/ai-cli-gateway/.github/workflows/release.yml \
  --source-ref refs/tags/v0.2.1
```

Only after the digest comparison succeeds, extract and place the binary in a user-owned directory:

```bash
tar -xzf "${ASSET}"
RELEASE_ROOT="${DOWNLOAD_DIR}/${ASSET%.tar.gz}"
GATEWAY_BIN_DIR="${HOME}/.local/bin"
mkdir -p "${GATEWAY_BIN_DIR}"
install -m 0755 "${RELEASE_ROOT}/ai-cli-gateway" \
  "${GATEWAY_BIN_DIR}/ai-cli-gateway"
PATH="${GATEWAY_BIN_DIR}:${PATH}"
export PATH
```

Create private gateway directories, point the copied Codex template at the already authenticated CLI home, and replace all four generic values. Set `AI_CLI_GATEWAY_CODEX_MODEL` to an accessible Codex model before this block. The fail-closed TOML policy rejects empty values, backslashes, double quotes, and every control character before replacement.

```bash
set -eu
umask 077
GATEWAY_CONFIG_DIR="${HOME}/.config/ai-cli-gateway"
GATEWAY_RUNTIME_DIR="${HOME}/.local/state/ai-cli-gateway/runtime"
CODEX_CONFIG_HOME="${HOME}/.codex"
CODEX_EXECUTABLE="$(command -v codex)"
CODEX_MODEL="${AI_CLI_GATEWAY_CODEX_MODEL:?set this to an accessible Codex model}"
validate_toml_value() {
  test "$#" -eq 1 || return 1
  case "$1" in
    ''|*\\*|*\"*|*[[:cntrl:]]*) return 1 ;;
  esac
}
for VALUE in "${GATEWAY_CONFIG_DIR}" "${GATEWAY_RUNTIME_DIR}" \
  "${CODEX_CONFIG_HOME}" "${CODEX_EXECUTABLE}"; do
  case "${VALUE}" in /*) ;; *) exit 1 ;; esac
done
validate_toml_value "${CODEX_EXECUTABLE}"
validate_toml_value "${CODEX_CONFIG_HOME}"
validate_toml_value "${GATEWAY_RUNTIME_DIR}"
validate_toml_value "${CODEX_MODEL}"
test -x "${CODEX_EXECUTABLE}"
test -d "${CODEX_CONFIG_HOME}"
test ! -L "${CODEX_CONFIG_HOME}"
test -O "${CODEX_CONFIG_HOME}"
mkdir -p "${GATEWAY_CONFIG_DIR}" "${GATEWAY_RUNTIME_DIR}"
chmod 700 "${GATEWAY_CONFIG_DIR}" "${GATEWAY_RUNTIME_DIR}" "${CODEX_CONFIG_HOME}"

for PRIVATE_DIR in "${GATEWAY_CONFIG_DIR}" "${GATEWAY_RUNTIME_DIR}" "${CODEX_CONFIG_HOME}"; do
  test -d "${PRIVATE_DIR}"
  test ! -L "${PRIVATE_DIR}"
  test -O "${PRIVATE_DIR}"
  case "$(uname -s)" in
    Darwin) test "$(stat -f '%u:%Lp' "${PRIVATE_DIR}")" = "$(id -u):700" ;;
    Linux) test "$(stat -c '%u:%a' "${PRIVATE_DIR}")" = "$(id -u):700" ;;
    *) exit 1 ;;
  esac
done

GATEWAY_CONFIG_FILE="${GATEWAY_CONFIG_DIR}/config.toml"
cp "${RELEASE_ROOT}/examples/config/codex.example.toml" "${GATEWAY_CONFIG_FILE}"
CONFIG_TEMP="$(mktemp "${GATEWAY_CONFIG_DIR}/config.toml.XXXXXX")"
export CODEX_EXECUTABLE CODEX_CONFIG_HOME GATEWAY_RUNTIME_DIR CODEX_MODEL
perl -0pe '
  my @pairs = (
    [q{/opt/ai-cli-gateway/bin/codex}, $ENV{CODEX_EXECUTABLE}],
    [q{/var/lib/ai-cli-gateway/codex-home}, $ENV{CODEX_CONFIG_HOME}],
    [q{/var/lib/ai-cli-gateway/runtime}, $ENV{GATEWAY_RUNTIME_DIR}],
    [q{configured-provider-model}, $ENV{CODEX_MODEL}],
  );
  for my $pair (@pairs) {
    my ($old, $new) = @{$pair};
    my $count = s/\Q$old\E/$new/g;
    die "template marker count" unless $count == 1;
  }
' "${GATEWAY_CONFIG_FILE}" > "${CONFIG_TEMP}"
chmod 600 "${CONFIG_TEMP}"
mv -f "${CONFIG_TEMP}" "${GATEWAY_CONFIG_FILE}"
chmod 600 "${GATEWAY_CONFIG_FILE}"

openssl rand -hex 32 > "${GATEWAY_CONFIG_DIR}/gateway.key"
chmod 600 "${GATEWAY_CONFIG_DIR}/gateway.key"
GATEWAY_KEY="$(LC_ALL=C tr -d '\n' < "${GATEWAY_CONFIG_DIR}/gateway.key")"
test "${#GATEWAY_KEY}" -eq 64
case "${GATEWAY_KEY}" in *[!0-9a-f]*) exit 1 ;; esac
export AI_CLI_GATEWAY_API_KEY="${GATEWAY_KEY}"

PATH="${HOME}/.local/bin:${PATH}"
export PATH
ai-cli-gateway doctor --config "${GATEWAY_CONFIG_FILE}"
```

Linux service operators can adapt the checked-in [systemd service example](../deploy/systemd/ai-cli-gateway.service) after completing the same path, ownership, Doctor, and credential checks.

In terminal 2, independently load and validate the external key, then serve:

```bash
set -eu
GATEWAY_CONFIG_DIR="${HOME}/.config/ai-cli-gateway"
GATEWAY_CONFIG_FILE="${GATEWAY_CONFIG_DIR}/config.toml"
GATEWAY_KEY="$(LC_ALL=C tr -d '\n' < "${GATEWAY_CONFIG_DIR}/gateway.key")"
test "${#GATEWAY_KEY}" -eq 64
case "${GATEWAY_KEY}" in *[!0-9a-f]*) exit 1 ;; esac
export AI_CLI_GATEWAY_API_KEY="${GATEWAY_KEY}"
PATH="${HOME}/.local/bin:${PATH}"
export PATH
ai-cli-gateway serve --config "${GATEWAY_CONFIG_FILE}"
```

In terminal 3, independently load the same key as data, create a non-sensitive request file, and call both endpoints:

```bash
set -eu
umask 077
GATEWAY_CONFIG_DIR="${HOME}/.config/ai-cli-gateway"
GATEWAY_KEY="$(LC_ALL=C tr -d '\n' < "${GATEWAY_CONFIG_DIR}/gateway.key")"
test "${#GATEWAY_KEY}" -eq 64
case "${GATEWAY_KEY}" in *[!0-9a-f]*) exit 1 ;; esac
export AI_CLI_GATEWAY_API_KEY="${GATEWAY_KEY}"

REQUEST_FILE="${GATEWAY_CONFIG_DIR}/quick-start-request.json"
printf '%s\n' '{"model":"codex-local","instructions":"Answer concisely.","input":"Reply with exactly: GATEWAY_OK","text":{"format":{"type":"text"}},"stream":false,"store":false,"tools":[],"tool_choice":"none"}' > "${REQUEST_FILE}"
chmod 600 "${REQUEST_FILE}"
curl --fail-with-body \
  -H @- \
  http://127.0.0.1:8080/v1/models <<EOF
Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY}
EOF
curl --fail-with-body \
  -H @- \
  -H 'Content-Type: application/json' \
  --data-binary "@${REQUEST_FILE}" \
  http://127.0.0.1:8080/v1/responses <<EOF
Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY}
EOF
```

### Windows PowerShell

Use PowerShell 7 as an unprivileged gateway identity. This downloads exactly the Windows archive and its manifest, requires one exact manifest record, compares the SHA-256 case-insensitively, and fails before extraction on a mismatch:

```powershell
$ErrorActionPreference = 'Stop'
$Version = '0.2.1'
$ArchiveName = 'ai-cli-gateway_0.2.1_windows_amd64.zip'
$DownloadDir = Join-Path ([IO.Path]::GetTempPath()) ("ai-cli-gateway-" + [Guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($DownloadDir) | Out-Null
$ArchivePath = Join-Path $DownloadDir $ArchiveName
$ManifestPath = Join-Path $DownloadDir 'SHA256SUMS'
$ReleaseURL = "https://github.com/krkarma777/ai-cli-gateway/releases/download/v$Version"
Invoke-WebRequest -Uri "$ReleaseURL/$ArchiveName" -OutFile $ArchivePath
Invoke-WebRequest -Uri "$ReleaseURL/SHA256SUMS" -OutFile $ManifestPath

$ArchivePattern = '^[0-9a-f]{64} \*' + [regex]::Escape($ArchiveName) + '$'
$ManifestMatches = @(Get-Content -LiteralPath $ManifestPath | Where-Object { $_ -cmatch $ArchivePattern })
if ($ManifestMatches.Count -ne 1) { throw 'expected exactly one archive checksum record' }
$ExpectedSHA = $ManifestMatches[0].Substring(0, 64)
if ($ExpectedSHA -cnotmatch '^[0-9a-f]{64}$') { throw 'invalid checksum record' }
$ActualSHA = (Get-FileHash -Algorithm SHA256 -LiteralPath $ArchivePath).Hash
if (-not [String]::Equals($ExpectedSHA, $ActualSHA, [StringComparison]::OrdinalIgnoreCase)) {
  throw 'archive checksum mismatch'
}
```

Optionally verify the same canonical attestation before extraction:

```powershell
gh.exe attestation verify $ArchivePath `
  --repo krkarma777/ai-cli-gateway `
  --predicate-type https://slsa.dev/provenance/v1 `
  --signer-workflow github.com/krkarma777/ai-cli-gateway/.github/workflows/release.yml `
  --source-ref refs/tags/v0.2.1
```

Extract to the temporary directory and copy the executable into a user-owned binary directory:

```powershell
Expand-Archive -LiteralPath $ArchivePath -DestinationPath $DownloadDir
$ReleaseRoot = Join-Path $DownloadDir 'ai-cli-gateway_0.2.1_windows_amd64'
$BinDir = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'AI CLI Gateway\bin'
[IO.Directory]::CreateDirectory($BinDir) | Out-Null
Copy-Item -LiteralPath (Join-Path $ReleaseRoot 'ai-cli-gateway.exe') `
  -Destination (Join-Path $BinDir 'ai-cli-gateway.exe') -Force
```

Windows TOML paths use forward slashes. `C:/Tools/Codex/codex.exe`, `C:/GatewayService/codex-home`, and `C:/GatewayService/runtime` are valid syntax examples only. Do not copy those illustrative locations blindly: choose private absolute locations owned by the gateway identity, point the executable at a real native Codex executable, and select an accessible model. The following user-profile locations avoid writing to a system directory; set `AI_CLI_GATEWAY_CODEX_EXE` and `AI_CLI_GATEWAY_CODEX_MODEL` first.

The setup block intentionally refuses to reuse an existing config directory, runtime directory, `config.toml`, or `gateway.key`. This makes ACL creation fail closed instead of preserving an unknown explicit ACE. After setup succeeds once, terminals 2 and 3 reuse the verified files without changing their ACLs.

```powershell
$ErrorActionPreference = 'Stop'
$GatewayConfigDir = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'AI CLI Gateway\config'
$GatewayRuntimeDir = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'AI CLI Gateway\runtime'
$CodexConfigHome = Join-Path ([Environment]::GetFolderPath('UserProfile')) '.codex'
$CodexExecutable = $env:AI_CLI_GATEWAY_CODEX_EXE
$CodexModel = $env:AI_CLI_GATEWAY_CODEX_MODEL
if (-not [IO.Path]::IsPathFullyQualified($CodexExecutable)) { throw 'set an absolute Codex executable' }
if (-not (Test-Path -LiteralPath $CodexExecutable -PathType Leaf)) { throw 'Codex executable not found' }
if (-not (Test-Path -LiteralPath $CodexConfigHome -PathType Container)) { throw 'Codex config home not found' }
if ([string]::IsNullOrWhiteSpace($CodexModel)) { throw 'set an accessible Codex model' }

$CodexExecutableTOML = $CodexExecutable.Replace('\', '/')
$CodexConfigHomeTOML = $CodexConfigHome.Replace('\', '/')
$GatewayRuntimeTOML = $GatewayRuntimeDir.Replace('\', '/')
$CodexModelTOML = $CodexModel
function Assert-SafeTOMLValue([string]$Value) {
  if ([string]::IsNullOrEmpty($Value)) { throw 'empty TOML substitution value' }
  foreach ($Character in $Value.ToCharArray()) {
    if ($Character -eq '"' -or $Character -eq '\' -or [char]::IsControl($Character)) {
      throw 'unsafe TOML substitution value'
    }
  }
}
Assert-SafeTOMLValue $CodexExecutableTOML
Assert-SafeTOMLValue $CodexConfigHomeTOML
Assert-SafeTOMLValue $GatewayRuntimeTOML
Assert-SafeTOMLValue $CodexModelTOML

$GatewayConfigFile = Join-Path $GatewayConfigDir 'config.toml'
$GatewayKeyPath = Join-Path $GatewayConfigDir 'gateway.key'
foreach ($FreshTarget in @($GatewayConfigDir, $GatewayRuntimeDir, $GatewayConfigFile, $GatewayKeyPath)) {
  if (Test-Path -LiteralPath $FreshTarget) { throw 'private target already exists' }
}
[IO.Directory]::CreateDirectory($GatewayConfigDir) | Out-Null
[IO.Directory]::CreateDirectory($GatewayRuntimeDir) | Out-Null
$CurrentSID = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
function Assert-ExactPrivateACL(
  [string]$Path,
  [Security.AccessControl.FileSystemRights]$ExpectedRights,
  [Security.AccessControl.InheritanceFlags]$ExpectedInheritance
) {
  $ACL = Get-Acl -LiteralPath $Path
  if (-not $ACL.AreAccessRulesProtected) { throw 'private ACL still inherits' }
  $OwnerSID = $ACL.GetOwner([Security.Principal.SecurityIdentifier]).Value
  if ($OwnerSID -ne $CurrentSID) { throw 'private ACL has another owner' }
  $Rules = @($ACL.Access)
  if ($Rules.Count -ne 1) { throw 'private ACL must contain exactly one rule' }
  $Rule = $Rules[0]
  $RuleSID = $Rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
  if ($Rule.IsInherited) { throw 'private ACL rule is inherited' }
  if ($RuleSID -ne $CurrentSID) { throw 'private ACL belongs to another identity' }
  if ($Rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow) { throw 'private ACL rule is not allow' }
  if ($Rule.FileSystemRights -ne $ExpectedRights) { throw 'private ACL rights differ' }
  if ($Rule.InheritanceFlags -ne $ExpectedInheritance) { throw 'private ACL inheritance flags differ' }
  if ($Rule.PropagationFlags -ne [Security.AccessControl.PropagationFlags]::None) { throw 'private ACL propagation differs' }
}
function Set-ExactPrivateACL(
  [string]$Path,
  [Security.AccessControl.FileSystemRights]$ExpectedRights,
  [Security.AccessControl.InheritanceFlags]$ExpectedInheritance
) {
  $ACL = Get-Acl -LiteralPath $Path
  $ACL.SetAccessRuleProtection($true, $false)
  foreach ($ExistingRule in @($ACL.Access)) {
    [void]$ACL.RemoveAccessRuleAll($ExistingRule)
  }
  $CurrentSIDObject = [Security.Principal.SecurityIdentifier]::new($CurrentSID)
  $ACL.SetOwner($CurrentSIDObject)
  $Rule = [Security.AccessControl.FileSystemAccessRule]::new(
    $CurrentSIDObject,
    $ExpectedRights,
    $ExpectedInheritance,
    [Security.AccessControl.PropagationFlags]::None,
    [Security.AccessControl.AccessControlType]::Allow
  )
  $ACL.SetAccessRule($Rule)
  Set-Acl -LiteralPath $Path -AclObject $ACL
  Assert-ExactPrivateACL $Path $ExpectedRights $ExpectedInheritance
}
function Set-ExactPrivateDirectoryACL([string]$Path) {
  $Inheritance = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor `
    [Security.AccessControl.InheritanceFlags]::ObjectInherit
  Set-ExactPrivateACL $Path ([Security.AccessControl.FileSystemRights]::FullControl) $Inheritance
}
function Set-ExactPrivateFileACL([string]$Path) {
  $Rights = [Security.AccessControl.FileSystemRights]::Read -bor `
    [Security.AccessControl.FileSystemRights]::Write -bor `
    [Security.AccessControl.FileSystemRights]::Synchronize
  Set-ExactPrivateACL $Path $Rights ([Security.AccessControl.InheritanceFlags]::None)
}
foreach ($PrivateDir in @($GatewayConfigDir, $GatewayRuntimeDir)) {
  Set-ExactPrivateDirectoryACL $PrivateDir
}

Copy-Item -LiteralPath (Join-Path $ReleaseRoot 'examples/config/codex.example.toml') `
  -Destination $GatewayConfigFile
$ConfigText = [IO.File]::ReadAllText($GatewayConfigFile)
function Replace-ExactlyOnce([string]$Text, [string]$Old, [string]$New) {
  if ([regex]::Matches($Text, [regex]::Escape($Old)).Count -ne 1) {
    throw "template marker count: $Old"
  }
  return $Text.Replace($Old, $New)
}
$ConfigText = Replace-ExactlyOnce $ConfigText '/opt/ai-cli-gateway/bin/codex' $CodexExecutableTOML
$ConfigText = Replace-ExactlyOnce $ConfigText '/var/lib/ai-cli-gateway/codex-home' $CodexConfigHomeTOML
$ConfigText = Replace-ExactlyOnce $ConfigText '/var/lib/ai-cli-gateway/runtime' $GatewayRuntimeTOML
$ConfigText = Replace-ExactlyOnce $ConfigText 'configured-provider-model' $CodexModelTOML
[IO.File]::WriteAllText($GatewayConfigFile, $ConfigText, [Text.UTF8Encoding]::new($false))
Set-ExactPrivateFileACL $GatewayConfigFile

$RandomBytes = [byte[]]::new(32)
[Security.Cryptography.RandomNumberGenerator]::Fill($RandomBytes)
$GatewayKey = [Convert]::ToHexString($RandomBytes).ToLowerInvariant()
[IO.File]::WriteAllText($GatewayKeyPath, $GatewayKey, [Text.UTF8Encoding]::new($false))
Set-ExactPrivateFileACL $GatewayKeyPath
$LoadedGatewayKey = [IO.File]::ReadAllText($GatewayKeyPath).Trim()
if ($LoadedGatewayKey -cnotmatch '^[0-9a-f]{64}$') { throw 'invalid gateway key file' }
$env:AI_CLI_GATEWAY_API_KEY = $LoadedGatewayKey

$env:Path = "$BinDir;$env:Path"
ai-cli-gateway.exe doctor --config $GatewayConfigFile
```

In PowerShell terminal 2, independently load and validate the external key, then serve:

```powershell
$GatewayConfigDir = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'AI CLI Gateway\config'
$GatewayConfigFile = Join-Path $GatewayConfigDir 'config.toml'
$GatewayKeyPath = Join-Path $GatewayConfigDir 'gateway.key'
$LoadedGatewayKey = [IO.File]::ReadAllText($GatewayKeyPath).Trim()
if ($LoadedGatewayKey -cnotmatch '^[0-9a-f]{64}$') { throw 'invalid gateway key file' }
$env:AI_CLI_GATEWAY_API_KEY = $LoadedGatewayKey
$BinDir = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'AI CLI Gateway\bin'
$env:Path = "$BinDir;$env:Path"
ai-cli-gateway.exe serve --config $GatewayConfigFile
```

In PowerShell terminal 3, independently load and validate the key, write the request without a secret, and call both endpoints:

```powershell
$GatewayConfigDir = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'AI CLI Gateway\config'
$GatewayKeyPath = Join-Path $GatewayConfigDir 'gateway.key'
$LoadedGatewayKey = [IO.File]::ReadAllText($GatewayKeyPath).Trim()
if ($LoadedGatewayKey -cnotmatch '^[0-9a-f]{64}$') { throw 'invalid gateway key file' }
$env:AI_CLI_GATEWAY_API_KEY = $LoadedGatewayKey
$RequestFile = Join-Path $GatewayConfigDir 'quick-start-request.json'
$RequestBody = '{"model":"codex-local","instructions":"Answer concisely.","input":"Reply with exactly: GATEWAY_OK","text":{"format":{"type":"text"}},"stream":false,"store":false,"tools":[],"tool_choice":"none"}'
[IO.File]::WriteAllText($RequestFile, $RequestBody, [Text.UTF8Encoding]::new($false))
('Authorization: Bearer {0}' -f $env:AI_CLI_GATEWAY_API_KEY) |
  curl.exe --fail-with-body `
    -H '@-' `
    http://127.0.0.1:8080/v1/models
('Authorization: Bearer {0}' -f $env:AI_CLI_GATEWAY_API_KEY) |
  curl.exe --fail-with-body `
    -H '@-' `
    -H 'Content-Type: application/json' `
    --data-binary "@$RequestFile" `
    http://127.0.0.1:8080/v1/responses
```

### Official SDK checks

Every release archive includes the official SDK sources, manifests, and locks under `examples/openai-sdk`. Run these from the POSIX request/SDK terminal after its bounded gateway-key load. The base URL must include `/v1`, and the model is the configured gateway alias, not a provider model name:

```bash
set -eu
RELEASE_ROOT="${RELEASE_ROOT:?set this to the absolute extracted release directory}"
case "${RELEASE_ROOT}" in /*) ;; *) exit 1 ;; esac
SDK_TMP_PARENT="${TMPDIR:-/tmp}"
SDK_WORK_ROOT="$(mktemp -d "${SDK_TMP_PARENT%/}/ai-cli-gateway-sdk.XXXXXX")"
chmod 700 "${SDK_WORK_ROOT}"

export AI_CLI_GATEWAY_BASE_URL="http://127.0.0.1:8080/v1"
export AI_CLI_GATEWAY_MODEL="codex-local"
export AI_CLI_GATEWAY_TIMEOUT_SECONDS="300"

python3.12 -m venv "${SDK_WORK_ROOT}/python"
"${SDK_WORK_ROOT}/python/bin/python" -m pip install --disable-pip-version-check --no-input \
  --requirement "${RELEASE_ROOT}/examples/openai-sdk/python/requirements.lock"
"${SDK_WORK_ROOT}/python/bin/python" \
  "${RELEASE_ROOT}/examples/openai-sdk/python/main.py"

mkdir "${SDK_WORK_ROOT}/javascript"
cp "${RELEASE_ROOT}/examples/openai-sdk/javascript/main.mjs" \
  "${RELEASE_ROOT}/examples/openai-sdk/javascript/package.json" \
  "${RELEASE_ROOT}/examples/openai-sdk/javascript/package-lock.json" \
  "${SDK_WORK_ROOT}/javascript/"
npm ci --ignore-scripts --prefix "${SDK_WORK_ROOT}/javascript"
node "${SDK_WORK_ROOT}/javascript/main.mjs"
```

`AI_CLI_GATEWAY_TIMEOUT_SECONDS` is optional, accepts only `1..300`, and defaults to 300 seconds for real CLI inference. The examples validate only `models.list()` and one non-streaming `responses.create()` call with retries disabled. That request asks the provider for exactly `SDK_GATEWAY_OK` and accepts only that text with zero or one trailing newline. Install dependencies only in the private temporary virtual environment and JavaScript directory, never in the extracted release tree.

You are responsible for installing and authenticating each provider CLI and for using it in accordance with its applicable terms.

## SDK contract recovery

The local SDK contract harness normally removes its private working directory.
An `sdk_contract_cleanup_failed` result can retain an owner-only `.sdk-contract-*` sibling
when the harness cannot prove that a process or group
is absent, or that the root still has its original identity. Before recovery,
ensure no recorded contract process remains. Then inspect it and remove only the exact retained directory—never its parent and never a wildcard. The command
never prints the retained path or underlying error.

## Next steps

- [API and operations reference](reference.md)
- [Security policy](../SECURITY.md)
- [Contributing](../CONTRIBUTING.md)
