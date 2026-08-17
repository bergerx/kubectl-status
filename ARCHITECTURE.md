# Architecture

This document maps the actors, internal components, and data flow in `kubectl-status`, for
contributors who need the high-level shape of the system before diving into a package. It's
intentionally short — see [README.md](README.md#design-principle) for the product philosophy,
[CONVENTIONS.md](CONVENTIONS.md) for output conventions, [TEMPLATE-API.md](TEMPLATE-API.md) for
the template extension surface, and [SECURITY.md](SECURITY.md#scope) for the security-relevant
concerns tied to the actors listed here. It also satisfies [OpenSSF Security Baseline
OSPS-SA-01.01](https://baseline.openssf.org/versions/2025-02-25#osps-sa-0101), which requires
mapping actors and actions. Keep this current as actors, components, or data flows change.

## Actors

- **End user** — runs `kubectl status <args>` as a `kubectl` plugin, same invocation shape as
  `kubectl get`/`describe`. Supplies command-line flags and, optionally, local override files (see
  below).
- **`kubectl-status` binary** — the `status` executable built from this repo (`cmd/main.go`,
  `pkg/input`, `pkg/plugin`). Runs entirely on the user's workstation/CI runner; a single local
  process with no daemon, server, or network listener of its own.
- **Kubernetes API server** — the only remote system the binary talks to, other than under
  `--local` (see below). Queried read-only (get/list/watch) to fetch the requested objects and,
  when rendering asks for related objects (`$.KubeGetFirst`, `$.Include`/`--deep`), their
  dependents (owners, Events, matching Services/Ingresses/Routes, node metrics, ...).
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
  (`.goreleaser.yml`) and the [krew](https://krew.sigs.k8s.io/) index. Builds and publishes
  release artifacts, signs them with `cosign`, and generates SBOMs; `krew-release-bot` then updates
  the krew-index plugin manifest (`.krew.yaml`) that end users install from.

## Internal components

The binary is organized as a pipeline of three packages, wired together in `cmd/main.go`'s
`RootCmd`/`Run`:

- **`cmd`** — builds the `cobra.Command`: registers `kubectl`-standard flags
  (`genericclioptions.ConfigFlags`, `genericclioptions.ResourceBuilderFlags`) plus
  `kubectl-status`-specific ones (`--include-*`, `--deep`/`--shallow`, `--short`, `--watch`,
  `--local`, ...), binds them into a per-invocation `viper.Viper`, and hands off to
  `plugin.Run`.
- **`pkg/input`** — `ResourceRepo` wraps `client-go`/`cli-runtime`'s `resource.Builder` to turn the
  CLI's `TYPE[.VERSION][.GROUP] [NAME | -l label]` arguments into the matching Kubernetes objects,
  normally via API discovery/get/list calls. Under `--local`, it instead parses the objects
  straight out of a manifest file (`--filename`) and never calls the API server. It also exposes
  the lookups templates use to pull in related objects at render time (e.g. events, owners).
- **`pkg/plugin`** — the render pipeline:
    - **`renderEngine`** parses the embedded templates once per invocation into two independent
      `text/template` trees: `embedded` (`pkg/plugin/templates/*.tmpl`, `go:embed`'d into the
      binary — one `<Kind>.tmpl` per supported Kind plus shared partials in `common.tmpl`), and
      `user`, a clone of `embedded` with `~/.kubectl-status/templates/<Kind>.tmpl` overlaid on top
      if present. Keeping them separate means a user override can redefine a shared partial
      without silently changing how built-in Kind templates render.
    - **`RenderableObject`** (`renderable.go`) is the `.` context every template executes against.
      Its methods (`template_functions_dynamic.go`) — `KubeGetFirst`, `Include`, `HealthSummary`,
      etc. — are how a template pulls in more than the one object it started with, issuing further
      read-only API calls as it renders.
    - **`findTemplateName`** dispatches an object to a template by Kind: `"<Kind>.<group>"` if
      registered, else the bare `"<Kind>"`, else the generic `"DefaultResource"` fallback (defined
      in `common.tmpl`) for any Kind — including arbitrary CRDs — without a dedicated template.
      `DefaultResource` additionally consults `defaultResourceDetectors`
      (`default_resource_detectors.go`) to recognize CRD shapes belonging to specific ecosystems
      (Crossplane, Gatekeeper) and render extra ecosystem-aware detail for them.
    - **`template_functions_static.go`** supplies the pure formatting/computation `FuncMap`
      (colors, duration rounding, diffing, ...) shared by every template, built on top of
      `go-sprout`/sprig.
- Rendered output is written directly to `stdout` (or `--short`'s one-line-per-resource form);
  see [CONVENTIONS.md](CONVENTIONS.md) for what shape that output follows.

The full list of Kind templates, shared partials, `FuncMap` functions, and `RenderableObject`
methods a template (built-in or user override) may depend on is enumerated in
[TEMPLATE-API.md](TEMPLATE-API.md).

## Data flow

```
end user
  │ invokes `kubectl status ...`
  ▼
kubectl-status binary ──reads──▶ kubeconfig/credentials
  │
  │ read-only API calls (get/list/watch), or a local manifest file under --local
  ▼
Kubernetes API server (skipped under --local)
  │
  │ cluster object data (status/spec/metadata)
  ▼
kubectl-status binary
  │ renders via Go templates (pkg/plugin/templates/*.tmpl),
  │ optionally including user overrides from
  │ ~/.kubectl-status/templates/<Kind>.tmpl; a template may issue
  │ further read-only API calls for related objects while rendering
  ▼
stdout (human-only output, see CONVENTIONS.md), or --short's one-line-per-resource form
```

Under `--watch`, the flow above repeats for each change event the API server streams back for the
requested object(s), switching to `--shallow` rendering to keep each update fast.

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
