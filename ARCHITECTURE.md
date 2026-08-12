# Architecture

This document maps the actors and actions in `kubectl-status`, to satisfy [OpenSSF Security
Baseline OSPS-SA-01.01](https://baseline.openssf.org/versions/2025-02-25#osps-sa-0101). It's
intentionally short — see [README.md](README.md#design-principle) for the product philosophy,
[CONVENTIONS.md](CONVENTIONS.md) for output conventions, and [SECURITY.md](SECURITY.md#scope) for
the security-relevant concerns tied to the actors listed here. Keep this current as actors or data
flows change.

## Actors

- **End user** — runs `kubectl status <args>` as a `kubectl` plugin, same invocation shape as
  `kubectl get`/`describe`. Supplies command-line flags and, optionally, local override files (see
  below).
- **`kubectl-status` binary** — the `status` executable built from this repo (`cmd/main.go`,
  `pkg/plugin`). Runs entirely on the user's workstation/CI runner; a single local, read-only
  process with no daemon, server, or network listener of its own.
- **Kubernetes API server** — the only remote system the binary talks to. Queried read-only (get/list/watch)
  to fetch the requested objects and, when rendering asks for related objects (`$.KubeGetFirst`,
  `$.Include`/`--deep`), their dependents.
- **kubeconfig / credentials** — the user's existing `kubeconfig` (via the standard `client-go`
  `genericclioptions.ConfigFlags` a kubectl plugin inherits), including whatever auth
  plugins/tokens/certs it references. The binary reads but never modifies this file, and doesn't
  cache or persist credentials of its own.
- **Local template overrides** — optional user-authored files under
  `~/.kubectl-status/templates/<Kind>.tmpl` and `~/.kubectl-status/abnormal-true-condition-types`
  (see [README.md § Scope and extending it](README.md#scope-and-extending-it)). These are local
  filesystem input the binary trusts as much as any local config file, but the *templates* also
  execute against untrusted cluster object data at render time — see the [Template API's stable
  surface](TEMPLATE-API.md) for what an override can depend on.
- **CI / release pipeline** — GitHub Actions (`.github/workflows/`) plus `goreleaser`
  (`.goreleaser.yaml`) and the [krew](https://krew.sigs.k8s.io/) index. Builds and publishes
  release artifacts, signs them with `cosign`, and generates SBOMs; `krew-release-bot` then updates
  the krew-index plugin manifest (`.krew.yaml`) that end users install from.

## Data flow

```
end user
  │ invokes `kubectl status ...`
  ▼
kubectl-status binary ──reads──▶ kubeconfig/credentials
  │
  │ read-only API calls (get/list/watch)
  ▼
Kubernetes API server
  │
  │ cluster object data (status/spec/metadata)
  ▼
kubectl-status binary
  │ renders via Go templates (pkg/plugin/templates/*.tmpl),
  │ optionally including user overrides from
  │ ~/.kubectl-status/templates/<Kind>.tmpl
  ▼
stdout (human-only output, see CONVENTIONS.md)
```

Separately, and asynchronously from any single invocation:

```
maintainer pushes a `vX.Y.Z` tag
  ▼
GitHub Actions: goreleaser (build, SBOM, cosign sign) ──▶ GitHub Release artifacts
  ▼
krew-release-bot ──▶ krew-index PR (.krew.yaml) ──▶ end user's `kubectl krew install/upgrade status`
```

## Security-relevant notes

These are cross-referenced, not repeated, from [SECURITY.md § Scope](SECURITY.md#scope):

- The binary is read-only against the API server and doesn't expose network services itself.
- User template overrides render alongside untrusted cluster object data, so template-rendering
  bugs (leaking, mishandling, or executing that data unsafely) are in scope as security issues.
- Credentials flow one way in (from kubeconfig into API calls); the binary doesn't write, log, or
  transmit them elsewhere.
- The release pipeline (`goreleaser`, cosign signing, krew manifest) is a supply-chain surface —
  see SECURITY.md for how issues there should be reported.
