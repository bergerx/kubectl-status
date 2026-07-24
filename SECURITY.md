# Security Policy

## Supported Versions

kubectl-status only supports the latest released version. Security fixes are
made against `master` and shipped in the next release; older versions are not
patched separately.

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
and renders output locally, and does not itself expose network services. Of
particular interest are issues such as:

- Template rendering bugs that could leak, mishandle, or execute untrusted
  data from cluster objects.
- Credential or secret exposure in rendered output or logs beyond what the
  user explicitly requested.
- Supply-chain issues in the build/release pipeline (e.g. `goreleaser`
  workflow, krew manifest).
