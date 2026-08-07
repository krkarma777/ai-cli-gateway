# Getting Started

Install AI CLI Gateway from the v0.1.0 release, connect one authenticated provider CLI, and send a first request.

## Quick Start

This v0.1.0 path installs one release archive without administrator privileges, verifies only the archive that was downloaded, and starts a Codex-backed local gateway.

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
