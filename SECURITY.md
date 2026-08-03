# Security Policy

## Supported code

Security fixes target the current `main` code line. Older snapshots and downstream forks may need their own assessment.

## Reporting a vulnerability

Use GitHub private vulnerability reporting at <https://github.com/krkarma777/ai-cli-gateway/security/advisories/new>. Please submit a private report, not a public issue.

Do not include real tokens, auth files, prompts, model outputs, provider stderr, account identity, or sensitive paths. Build a minimal reproduction with the fake CLI and synthetic values whenever possible. Describe the affected version, platform, expected security boundary, and reproducible behavior without copying sensitive data.

If a defect exists only in an installed CLI or its hosted service, report it to the applicable upstream provider. Gateway containment, validation, redaction, or adapter defects belong here even when an upstream CLI is involved.

The project does not promise a particular response or remediation timetable. Public discussion can follow after a coordinated disclosure is safe.
