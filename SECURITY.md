# Security Policy

## Supported Versions

kubectl-status only supports the latest released version. Security fixes are
made against `master` and shipped in the next release; older versions are not
patched separately.

## Project Continuity

kubectl-status is maintained by a single person, with no formal succession
plan. If the project goes dormant, forking is the expected path for continued
maintenance.

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues,
discussions, or pull requests.**

Instead, report them privately by:

- Using GitHub's [private vulnerability reporting](https://github.com/bergerx/kubectl-status/security/advisories/new), or
- Emailing **bekirdo at gmail.com**

Please include as much of the following as you can:

- A description of the vulnerability and its potential impact.
- Steps to reproduce, including the command(s) run and, if applicable, a
  minimal (sanitized) Kubernetes manifest or `-o yaml` object that triggers
  the issue.
- The `kubectl status --version` and `kubectl version -o yaml` output.
- Any relevant logs (feel free to redact sensitive cluster data).

You should receive a response within a few days. If the issue is confirmed,
we will work on a fix and coordinate disclosure timing with you before any
public release notes or advisory are published.

## Scope

kubectl-status is a read-only `kubectl` plugin: it queries the Kubernetes API
and renders output locally, and does not itself expose network services. See
[ARCHITECTURE.md](ARCHITECTURE.md) for the actors and data flow behind this.
Of particular interest are issues such as:

- Template rendering bugs that could leak, mishandle, or execute untrusted
  data from cluster objects.
- Credential or secret exposure in rendered output or logs beyond what the
  user explicitly requested.
- Supply-chain issues in the build/release pipeline (e.g. `goreleaser`
  workflow, krew manifest).

## Secrets and Credentials Management Policy

This section documents how secrets and credentials used by the project are
stored, accessed, controlled, and rotated. It satisfies OpenSSF Baseline
criterion [OSPS-BR-07.02](https://baseline.openssf.org/versions/2025-02-25#osps-br-0702).

### Credentials Used by the Project

| Credential | Purpose | Storage / Access | Rotation |
|------------|---------|------------------|----------|
| **GitHub Actions `GITHUB_TOKEN`** | Authenticate CI workflows to push releases, create tags, publish to krew-index | Ephemeral, auto-provided by GitHub Actions per workflow run. Scoped via `permissions:` blocks in `.github/workflows/*.yml` (least privilege: `contents: write`, `id-token: write` only where needed). | Automatic — new token per workflow run. No long-lived token exists. |
| **cosign keyless OIDC signing** | Sign release artifacts (checksums, binaries, SBOMs) | No stored private key. Uses GitHub Actions' OIDC identity (`https://token.actions.githubusercontent.com`) to request a short-lived certificate from Fulcio at release time. The certificate binds to the workflow (`release.yml`) and the git tag ref (`refs/tags/vX.Y.Z`). | Automatic — new certificate per release. No long-lived signing key to rotate. |
| **krew-index bot token** | Update the krew plugin manifest in the krew-index repo | Stored as a GitHub Actions `secret` (`KREW_BOT_TOKEN`) in this repo's settings, used only by the `krew-release-bot` step in `release.yml`. | Rotated by the maintainer if/when the krew-index bot credential is refreshed. |

**No other credentials** (API keys, cloud provider tokens, database passwords, etc.) are used, stored, or accessed by this project's CI/CD or release pipeline.

### Preventing Accidental Secret Leakage

- **gitleaks** runs at two layers:
  - Pre-push hook (installed via `.pre-commit-config.yaml` → `make-security-check` hook) — scans staged commits before they leave the workstation.
  - CI workflow (`.github/workflows/security-checks.yml`) — scans full history on every PR, push to `master`, and daily schedule.
- Synthetic test fixtures in `tests/artifacts/` that trip gitleaks rules are allowlisted by *fingerprint only* in the committed `.gitleaksignore`. The fingerprint contains no secret material. Adding a new allowlist entry requires running `make gitleaks-allow` and reviewing the diff — this deliberate friction prevents reflexively silencing a real leak.

### Incident Response: Leaked Secret Discovered

If a contributor or maintainer discovers a committed secret (real, not a test fixture):

1. **Do not** push a commit that merely deletes the secret — it remains in git history.
2. **Immediately rotate** the compromised credential at its source (e.g., revoke the GitHub token, regenerate the API key).
3. **Report** the incident via the project's vulnerability reporting process (see [Reporting a Vulnerability](#reporting-a-vulnerability) above), even if the secret has been rotated — we track exposure for downstream impact assessment.
4. **History rewrite** (e.g., `git filter-repo` or BFG Repo-Cleaner) may be needed to purge the secret from git history. Coordinate with the maintainer before force-pushing rewritten history to `master`.
5. After rotation and history cleanup, verify `make gitleaks` passes clean.
