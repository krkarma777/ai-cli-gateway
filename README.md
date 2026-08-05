# AI CLI Gateway

AI CLI Gateway turns locally authenticated AI CLIs into an OpenAI Responses-compatible API.

It deliberately implements a small **Responses API-compatible subset**, not full OpenAI API compatibility. The gateway is a local, final-output bridge with strict validation; it is not a drop-in implementation of every OpenAI endpoint or feature.

The contract baseline is 2026-07-30, with the external provider transition notes below rechecked on 2026-08-02. The project supports locally prepared Codex CLI and Claude Code profiles, plus the three documented Gemini environment/external credential shapes; actual provider access remains an upstream decision.

## Quick Start

This v0.1.0 path installs one release archive without administrator privileges, verifies only the archive that was downloaded, and starts a Codex-backed local gateway. It exercises the documented non-streaming **Responses API-compatible subset**; it is not a claim of complete OpenAI API or SDK compatibility.

Choose the one archive matching the machine:

| Host | Release asset |
|---|---|
| Linux x86-64 | `ai-cli-gateway_0.1.0_linux_amd64.tar.gz` |
| Linux ARM64 | `ai-cli-gateway_0.1.0_linux_arm64.tar.gz` |
| macOS Intel | `ai-cli-gateway_0.1.0_darwin_amd64.tar.gz` |
| macOS Apple silicon | `ai-cli-gateway_0.1.0_darwin_arm64.tar.gz` |
| Windows x86-64 | `ai-cli-gateway_0.1.0_windows_amd64.zip` |

The commands below never print or source the gateway key. Install and log in with the official Codex CLI first. The model placeholder is not a provider default or entitlement claim: select a model that the authenticated CLI account can actually access. The gateway never copies Codex authentication files.

### POSIX (macOS and Linux)

Run the following in a Bash or Zsh terminal. It selects one of the four POSIX archives, downloads that archive plus the six-record manifest, selects exactly the archive's one manifest record, and compares its digest before extraction:

```bash
set -eu
VERSION=0.1.0
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
  --source-ref refs/tags/v0.1.0
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

Linux service operators can adapt the checked-in [systemd service example](deploy/systemd/ai-cli-gateway.service) after completing the same path, ownership, Doctor, and credential checks.

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
  -H "Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}" \
  http://127.0.0.1:8080/v1/models
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}" \
  --data-binary "@${REQUEST_FILE}" \
  http://127.0.0.1:8080/v1/responses
```

### Windows PowerShell

Use PowerShell 7 as an unprivileged gateway identity. This downloads exactly the Windows archive and its manifest, requires one exact manifest record, compares the SHA-256 case-insensitively, and fails before extraction on a mismatch:

```powershell
$ErrorActionPreference = 'Stop'
$Version = '0.1.0'
$ArchiveName = 'ai-cli-gateway_0.1.0_windows_amd64.zip'
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
  --source-ref refs/tags/v0.1.0
```

Extract to the temporary directory and copy the executable into a user-owned binary directory:

```powershell
Expand-Archive -LiteralPath $ArchivePath -DestinationPath $DownloadDir
$ReleaseRoot = Join-Path $DownloadDir 'ai-cli-gateway_0.1.0_windows_amd64'
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
$CurrentIdentity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
$CurrentSID = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
function Assert-ExactPrivateACL(
  [string]$Path,
  [Security.AccessControl.FileSystemRights]$ExpectedRights,
  [Security.AccessControl.InheritanceFlags]$ExpectedInheritance
) {
  $ACL = Get-Acl -LiteralPath $Path
  if (-not $ACL.AreAccessRulesProtected) { throw 'private ACL still inherits' }
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
function Assert-ExactPrivateDirectoryACL([string]$Path) {
  $Inheritance = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor `
    [Security.AccessControl.InheritanceFlags]::ObjectInherit
  Assert-ExactPrivateACL $Path ([Security.AccessControl.FileSystemRights]::FullControl) $Inheritance
}
function Assert-ExactPrivateFileACL([string]$Path) {
  $Rights = [Security.AccessControl.FileSystemRights]::Read -bor `
    [Security.AccessControl.FileSystemRights]::Write -bor `
    [Security.AccessControl.FileSystemRights]::Synchronize
  Assert-ExactPrivateACL $Path $Rights ([Security.AccessControl.InheritanceFlags]::None)
}
foreach ($PrivateDir in @($GatewayConfigDir, $GatewayRuntimeDir)) {
  & icacls.exe $PrivateDir /inheritance:r /grant:r "${CurrentIdentity}:(OI)(CI)(F)" | Out-Null
  if ($LASTEXITCODE -ne 0) { throw 'failed to protect private directory ACL' }
  Assert-ExactPrivateDirectoryACL $PrivateDir
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
& icacls.exe $GatewayConfigFile /inheritance:r /grant:r "${CurrentIdentity}:(R,W)" | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'failed to protect config ACL' }
Assert-ExactPrivateFileACL $GatewayConfigFile

$RandomBytes = [byte[]]::new(32)
[Security.Cryptography.RandomNumberGenerator]::Fill($RandomBytes)
$GatewayKey = [Convert]::ToHexString($RandomBytes).ToLowerInvariant()
[IO.File]::WriteAllText($GatewayKeyPath, $GatewayKey, [Text.UTF8Encoding]::new($false))
& icacls.exe $GatewayKeyPath /inheritance:r /grant:r "${CurrentIdentity}:(R,W)" | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'failed to protect gateway key ACL' }
Assert-ExactPrivateFileACL $GatewayKeyPath
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
curl.exe --fail-with-body `
  -H "Authorization: Bearer $env:AI_CLI_GATEWAY_API_KEY" `
  http://127.0.0.1:8080/v1/models
curl.exe --fail-with-body `
  -H 'Content-Type: application/json' `
  -H "Authorization: Bearer $env:AI_CLI_GATEWAY_API_KEY" `
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

`AI_CLI_GATEWAY_TIMEOUT_SECONDS` is optional, accepts only `1..300`, and defaults to 300 seconds for real CLI inference. The examples validate only `models.list()` and one non-streaming `responses.create()` call with retries disabled. That request asks the provider for exactly `SDK_GATEWAY_OK` and accepts only that text with zero or one trailing newline. The CI harness overrides the timeout to five seconds for its deterministic fake CLI. Install dependencies only in the private temporary virtual environment and JavaScript directory, never in the extracted release tree.

You are responsible for installing and authenticating each provider CLI and for using it in accordance with its applicable terms.

## Architecture and scope

```text
Client
  -> POST /v1/responses
  -> AI CLI Gateway
  -> Codex / Claude Code / Gemini CLI adapter
  -> final text or locally validated JSON
```

Only these endpoints are implemented, and both are non-streaming:

- `POST /v1/responses` runs one configured provider adapter and returns a completed response.
- `GET /v1/models` returns an immutable configured alias snapshot and never starts a provider CLI. A listed alias can still return `503 provider_not_ready` when used.

There is no response retrieval endpoint, SSE streaming, tool-call round trip, session or conversation store, web UI, or database. Provider sessions are disabled where the pinned CLI exposes that control; the gateway itself stores no conversation state.

## Request contract

The top-level request subset is closed:

| Field | Supported subset |
|---|---|
| `model` | required nonempty configured alias string |
| `input` | required nonempty UTF-8 string |
| `instructions` | optional UTF-8 string or `null` |
| `text.format` | `text` or the strict `json_schema` profile below |
| `stream` | absent or exactly `false` |
| `store` | absent or exactly `false` |
| `tools` | absent or exactly `[]` |
| `tool_choice` | absent or exactly `"none"` |

Unknown fields and unsupported values return `400 unsupported_parameter`; they are never ignored. Duplicate keys, malformed JSON, a trailing JSON value, excessive data, and invalid field types are also rejected deterministically.

Unsupported inputs include array or multimodal `input`, nonempty `tools`, streaming, `previous_response_id` or other prior-response/conversation identifiers, `metadata`, `reasoning`, generation controls, provider-specific options, background execution, and stored responses. Setting `store` or `stream` to `true` is unsupported.

### Portable JSON Schema profile

A `json_schema` request has an object root and may use the seven types `object`, `array`, `string`, `number`, `integer`, `boolean`, and `null`. The closed keyword set is:

- `type`, `properties`, `required`, `additionalProperties`, and `items`;
- `enum` and `const`;
- `minLength`, `maxLength`, `minItems`, `maxItems`, `minProperties`, and `maxProperties`;
- `minimum`, `maximum`, `exclusiveMinimum`, and `exclusiveMaximum`; and
- `description` and `title`.

Every object schema must use `additionalProperties:false`, and every property must appear in `required`. There are no references, unions or combinators, patterns, formats, remote resolution, or Markdown-fence extraction. A provider must return exactly one JSON object that is duplicate-free. AI CLI Gateway validates it locally and performs no repair, fallback, or retry.

Structured JSON remains a validated string in `output_text.text`; AI CLI Gateway does not invent an `output_json` field.

## Requests and responses

The optional gateway key is read from the environment variable named by `server.api_key_env`. Put request data in a file so prompts and keys do not become command-line arguments.

### Text request

Save this synthetic body as `request.json`:

```json
{
  "model": "codex-local",
  "instructions": "Answer concisely.",
  "input": "Return a short greeting.",
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

Then send it with the key supplied by the caller environment:

```bash
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}" \
  --data-binary @request.json \
  http://127.0.0.1:8080/v1/responses
```

### JSON Schema request

Replace `request.json` with this portable-profile body:

```json
{
  "model": "claude-local",
  "input": "Return one synthetic status value.",
  "text": {
    "format": {
      "type": "json_schema",
      "name": "status_result",
      "strict": true,
      "schema": {
        "type": "object",
        "properties": {
          "status": {
            "type": "string",
            "enum": ["ready", "waiting"]
          }
        },
        "required": ["status"],
        "additionalProperties": false
      }
    }
  }
}
```

Send it with the same safe invocation:

```bash
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}" \
  --data-binary @request.json \
  http://127.0.0.1:8080/v1/responses
```

### Complete success response

IDs and timestamps are generated per request. A text response has this complete stable shape:

```json
{
  "id": "resp_aaaaaaaaaaaaaaaaaaaaaaaaaa",
  "object": "response",
  "created_at": 1785369600,
  "completed_at": 1785369601,
  "status": "completed",
  "background": false,
  "error": null,
  "incomplete_details": null,
  "instructions": null,
  "model": "codex-local",
  "output": [
    {
      "id": "msg_bbbbbbbbbbbbbbbbbbbbbbbbbb",
      "type": "message",
      "status": "completed",
      "role": "assistant",
      "content": [
        {
          "type": "output_text",
          "annotations": [],
          "text": "Final provider output"
        }
      ]
    }
  ],
  "parallel_tool_calls": false,
  "previous_response_id": null,
  "store": false,
  "text": {
    "format": {
      "type": "text"
    }
  },
  "tools": [],
  "tool_choice": "none"
}
```

### Models

Use the same Bearer convention for the immutable model-alias snapshot:

```bash
curl --fail-with-body \
  -H "Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}" \
  http://127.0.0.1:8080/v1/models
```

A complete list response is:

```json
{
  "object": "list",
  "data": [
    {
      "id": "codex-local",
      "object": "model",
      "created": 0,
      "owned_by": "local"
    }
  ]
}
```

### Stable errors

Errors never include provider output or raw child-process diagnostics. For example, an unsupported value returns this exact envelope shape:

```json
{
  "error": {
    "message": "This parameter or value is not supported.",
    "type": "invalid_request_error",
    "param": "stream",
    "code": "unsupported_parameter"
  }
}
```

The stable catalog is:

| HTTP | Codes |
|---:|---|
| 400 | `invalid_json`, `invalid_request`, `unsupported_parameter`, `invalid_json_schema` |
| 401 | `invalid_bearer_key` |
| 404 | `not_found`, `model_not_found` |
| 405 | `method_not_allowed` |
| 408 | `request_timeout` |
| 413 | `request_too_large` |
| 415 | `unsupported_media_type` |
| 429 | `server_busy`, `queue_full`, `provider_rate_limited` |
| 500 | `process_cleanup_failed`, `internal_error` |
| 502 | `output_limit_exceeded`, `provider_protocol_error`, `structured_output_invalid`, `provider_failed` |
| 503 | `queue_timeout`, `provider_not_ready`, `provider_auth_required`, `service_shutting_down` |
| 504 | `provider_timeout` |

## Build and commands

Go 1.26.5 is required. A release-style local build writes outside the source tree:

```bash
CGO_ENABLED=0 go build -trimpath -o "${TMPDIR:-/tmp}/ai-cli-gateway" ./cmd/ai-cli-gateway
```

The public command grammar is exact:

```text
usage:
  ai-cli-gateway version
  ai-cli-gateway serve --config PATH
  ai-cli-gateway doctor --config PATH [--json]
```

Both JSON Doctor orders are accepted: `ai-cli-gateway doctor --config PATH --json` and `ai-cli-gateway doctor --json --config PATH`. The equals-sign form is intentionally not part of the grammar.

Help is available as `ai-cli-gateway --help`, `ai-cli-gateway version --help`, `ai-cli-gateway serve --help`, and `ai-cli-gateway doctor --help`.

The exit status is 0 for success or a clean handled shutdown, 1 for readiness, runtime, serve, or cleanup failure, and 2 for usage or configuration failure. Closed CLI diagnostics include `configuration_invalid`, `gateway_not_ready: run ai-cli-gateway doctor`, `doctor_failed`, and `serve_failed: run ai-cli-gateway doctor`. `doctor` performs no inference and emits deterministic, redacted text or JSON.

## Configuration and providers

Start from `config.example.toml`. It is a Unix/systemd deployment example with generic paths and all normalized defaults. Install and authenticate each official CLI yourself under a dedicated gateway OS user and dedicated config home; set each configured executable to an absolute path.

Provider compatibility guards are compiled in and are not configurable:

| Provider | Pinned range | Adapter status | Live status | Runtime readiness |
|---|---|---|---|---|
| Codex | Codex `>=0.146.0,<0.147.0` | `implemented` | `live-verified`: not run | `not-ready` or ready only as reported by Doctor; initially unassessed |
| Claude | Claude Code `>=2.1.208,<2.2.0` | `implemented` | `live-verified`: not run | `not-ready` or ready only as reported by Doctor; initially unassessed |
| Gemini | Gemini CLI `>=0.53.0,<0.54.0` | `implemented` | `live-verified`: not run | `not-ready` or ready only as reported by Doctor; initially unassessed |

Here, `implemented` means the adapter command/parser and fake integration passed. `live-verified` means a pinned official CLI passed the explicit opt-in inference contract; no such run is claimed above. `not-ready` means operator-specific version, authentication, path, or capability checks fail. Run Doctor on the deployment host to establish readiness. `serve` requires the core checks and at least one ready provider; a zero-ready startup fails closed and releases the runtime root.

Codex uses its prepared dedicated config home and accepts no credential environment relay. Claude can use its dedicated authenticated config home or the explicitly selected `ANTHROPIC_API_KEY` mode. Gemini accepts exactly one of these local credential shapes:

- `GEMINI_API_KEY`;
- `GOOGLE_API_KEY`; or
- the complete Vertex profile: `GOOGLE_APPLICATION_CREDENTIALS`, `GOOGLE_CLOUD_PROJECT`, and `GOOGLE_CLOUD_LOCATION`.

Every Gemini request gets a disposable `GEMINI_CLI_HOME`; cached personal OAuth reuse is unsupported. Naming one of these shapes proves only local configuration acceptance. It does not establish upstream availability, billing tier, quota, entitlement, or live credential validity.

Codex readiness uses five isolated, non-inference probes. Its `doctor --json`
report is eligible only when the runner succeeds, the command exits exactly 0
or 1, and the complete output is one bounded, duplicate-free JSON value with
`schemaVersion:1`, an `overallStatus` in `ok`/`warn`/`fail`, and exact
`auth.credentials` and `config.load` checks whose IDs match their keys and whose
statuses are `ok`. The installation and all other checks are ignored only after
the entire JSON value has passed safe parsing. In particular, an npm-prefix
installation check run with the gateway's isolated request `HOME` is not an
authoritative gateway-readiness signal.

Codex publishes its complete capability set only when the exec help, hardening
feature list, and eligible doctor report all pass. A doctor-only failure retains
the already established version and coarse login status, but publishes no
capabilities and adds `capability_missing` (plus any independent auth problem).
These readiness checks do not prove npm package provenance, installation
integrity, or that the configured launcher came from an official package.

When an exact Unix Node launcher is used, provider children run with the pinned absolute Node interpreter and launcher identities. The child environment is minimal: arbitrary proxy, custom-CA, keyring, shell, and other ambient variables are not inherited. A deployment that needs another value requires a future explicitly validated allowlist.

### Unix Node launchers

An absolute provider executable may resolve to a Node launcher whose first line
is exactly #!/usr/bin/env node with LF or CRLF. At startup, Doctor resolves node
once from the startup PATH, applies the same executable and ancestor safety
checks, and pins the absolute Node and launcher identities. Provider children
still receive a rebuilt safe path; the ambient PATH is not inherited. A missing
or unsafe Node candidate reports `executable_unsafe` before probing.

On Unix, every `config_home` must be an absolute non-symlink directory owned by
the gateway effective user with exact mode `0700`.

### Windows paths

On Windows, use drive-absolute or UNC paths for the executable, config home, external credential file, and runtime root. A native CLI executable uses empty `prefix_args`. A Node-distributed CLI uses an absolute `node.exe` executable and exactly one absolute `.js` or `.mjs` entrypoint in `prefix_args`. The committed example remains the Unix/systemd form.

## Gemini upstream transition boundary

Google announced a Gemini CLI transition on [2026-05-19](https://developers.googleblog.com/an-important-update-transitioning-gemini-cli-to-antigravity-cli/) and then announced the consumer change effective [2026-06-18](https://github.com/google-gemini/gemini-cli/discussions/28017). Its [consumer deprecation notice](https://developers.google.com/gemini-code-assist/docs/deprecations/code-assist-individuals) says Google stopped the consumer Login-with-Google path for Gemini Code Assist for individuals, Google AI Pro, and Google AI Ultra and points those users to Antigravity.

Google says Code Assist Standard and Enterprise plus paid API-key access remain, while the current [API-key and Vertex tiers documentation](https://geminicli.com/docs/resources/quota-and-pricing/) also describes other paid and unpaid quota shapes. Those descriptions are not exhaustive gateway access rules. As of 2026-08-02, actual availability, billing tier, quota, entitlement, and live credential validity are exclusively upstream; provider execution is authoritative. The gateway's `configured`, `implemented`, and readiness states prove local checks only. Antigravity CLI is out of scope.

## Operational defaults

Unless overridden within validated bounds, every provider has concurrency 1, queue 32, queued bytes 16 MiB, queue wait 30 seconds, and execution timeout 300 seconds. Process limits are TERM grace 2 seconds, cleanup 5 seconds, stdout 2 MiB, stderr 256 KiB, and final output 1 MiB. Request limits are HTTP body 1 MiB, input 512 KiB, instructions 256 KiB, and schema 32 KiB.

AI CLI Gateway makes exactly one adapter attempt: there is no gateway retry, no fallback, and no provider switching. A provider CLI may perform provider-internal network retries that the gateway cannot observe or eliminate. Cancellation and the execution deadline bound local duration, but they cannot prove one upstream billable attempt; opt-in inference can incur usage and cost.

The listener accepts loopback literals only and defaults to `127.0.0.1:8080`. Bearer authentication is optional; when enabled, the value is read only from the configured environment name and compared without timing-sensitive string equality. Callers are trusted at the same-OS-user boundary, so a dedicated service OS user is recommended.

Provider binaries are absolute validated paths. Processes are started from argv arrays without a shell, and the prompt is passed through stdin. Each admitted request receives a `0700` temporary runtime and `0600` request files. One process owns the runtime root exclusively, and configuration, aliases, provider readiness, and the key are immutable startup snapshots; there is no hot reload.

The gateway does not issue, discover, extract, copy, parse, refresh, or store login tokens. It only relays an explicitly allowlisted credential value in child memory. It does not log any prompt, output, schema, credentials, raw stdout or stderr, full argv, environment, config path, or authentication identity.

`instructions` is a separately length-framed prompt section. Its priority is provider-dependent; it is not an enforceable OpenAI-style developer-message isolation boundary against adversarial `input`.

### Shutdown and containment

SIGINT or SIGTERM stops admission. The HTTP listener is closed and that closure is observed before Gateway shutdown and scheduler/supervisor drain begins. HTTP handling has a hard HTTP graceful-shutdown period; expiry triggers a bounded force close. The process containment ownership is then drained before exit even when that safety-first drain exceeds the network grace, followed by the final janitor and runtime-root release.

Unix starts each provider in a new process group. A descendant that deliberately calls `setsid`, power loss, or an uncatchable `SIGKILL` of the gateway is outside that Unix guarantee. Windows uses a non-breakaway, kill-on-close Job Object. Under systemd, `KillMode=control-group` adds a service-manager boundary; it does not replace gateway cleanup. A cleanup invariant failure is redacted, makes the provider unavailable, and produces a nonzero result or exit.

## Opt-in live contract tests

Default tests and CI use fake executables. They neither inspect nor invoke an installed provider CLI. The live sources can be compiled without execution with:

```bash
go test -tags=live -run '^$' ./internal/provider/...
```

Live tests are operator-triggered and may incur provider usage and cost. Probe execution first requires `AI_CLI_GATEWAY_LIVE_PROBES` with value `1`. Inference additionally requires `AI_CLI_GATEWAY_LIVE_INFERENCE` with value `1` and exactly the matching provider gate with value `1`: `AI_CLI_GATEWAY_LIVE_CODEX_INFERENCE`, `AI_CLI_GATEWAY_LIVE_CLAUDE_INFERENCE`, or `AI_CLI_GATEWAY_LIVE_GEMINI_INFERENCE`.

Each selected provider also needs its dedicated canary configuration:

- Codex: `AI_CLI_GATEWAY_LIVE_CODEX_EXECUTABLE`, `AI_CLI_GATEWAY_LIVE_CODEX_CONFIG_HOME`, and `AI_CLI_GATEWAY_LIVE_CODEX_MODEL`.
- Claude: `AI_CLI_GATEWAY_LIVE_CLAUDE_EXECUTABLE`, `AI_CLI_GATEWAY_LIVE_CLAUDE_CONFIG_HOME`, `AI_CLI_GATEWAY_LIVE_CLAUDE_MODEL`, and `AI_CLI_GATEWAY_LIVE_CLAUDE_AUTH_MODE=config_home|api_key`.
- Gemini: `AI_CLI_GATEWAY_LIVE_GEMINI_EXECUTABLE`, `AI_CLI_GATEWAY_LIVE_GEMINI_CONFIG_HOME`, `AI_CLI_GATEWAY_LIVE_GEMINI_MODEL`, and `AI_CLI_GATEWAY_LIVE_GEMINI_AUTH_MODE=gemini_api_key|google_api_key|vertex`.

The selected API-key or Vertex mode also requires its corresponding provider environment values outside the repository. Use a dedicated disposable canary; the harness redacts failures and cleans up even when a check fails. Live verification has not been run for this README and remains `not run`.

GitHub Actions uses Node24-based official actions. GitHub-hosted runners meet that runtime automatically; a self-hosted runner needs `actions/runner` v2.327.1 or later.

### SDK contract recovery

The local SDK contract harness normally removes its private working directory.
An `sdk_contract_cleanup_failed` result can retain an owner-only `.sdk-contract-*` sibling
when the harness cannot prove that a process or group
is absent, or that the root still has its original identity. Before recovery,
ensure no recorded contract process remains. Then inspect it and remove only the exact retained directory—never its parent and never a wildcard. The command
never prints the retained path or underlying error.

## Security and terms

See [SECURITY.md](SECURITY.md) for private vulnerability reporting. The gateway reduces accidental exposure and owns child cleanup, but it is not an isolation boundary between mutually untrusted users sharing an OS account.

You are responsible for installing and authenticating each provider CLI and for using it in accordance with its applicable terms.

## Official contract sources

The 2026-07-30 implementation baseline was checked against these official contracts, with the dated Gemini transition rechecked on 2026-08-02:

- OpenAI: [create a response](https://developers.openai.com/api/reference/resources/responses/methods/create), [text generation](https://developers.openai.com/api/docs/guides/text), [Structured Outputs](https://developers.openai.com/api/docs/guides/structured-outputs), and [list models](https://developers.openai.com/api/reference/resources/models/methods/list).
- Codex: [non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode), [`codex exec`](https://learn.chatgpt.com/docs/developer-commands?surface=cli#cli-codex-exec), and [authentication](https://learn.chatgpt.com/docs/auth).
- Claude Code: [headless mode](https://code.claude.com/docs/en/headless), [CLI reference](https://code.claude.com/docs/en/cli-usage), [result types](https://code.claude.com/docs/en/agent-sdk/typescript), [environment variables](https://code.claude.com/docs/en/env-vars), [authentication](https://code.claude.com/docs/en/authentication), and [changelog](https://code.claude.com/docs/en/changelog).
- Gemini CLI: [headless mode](https://geminicli.com/docs/cli/headless/), [CLI reference](https://geminicli.com/docs/cli/cli-reference/), [configuration](https://geminicli.com/docs/reference/configuration/), [authentication](https://geminicli.com/docs/get-started/authentication/), and [session management](https://geminicli.com/docs/cli/session-management/).
