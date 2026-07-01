---
name: generate-template
description: >
  Use when the user asks to create a kubectl-status template for one or more Kubernetes
  resource kinds found in the current kubectl context.
---

# kubectl-status Template Generator

Generate kubectl-status Go template files for resource kinds and write them to
`~/.kubectl-status/templates/<Kind>.tmpl`.

## Reference material — read before writing

- **`CONVENTIONS.md`** — output philosophy, color rules, and all template design conventions. Read this first.
- **`pkg/plugin/templates/common.tmpl`** — all shared sub-templates and available functions.
- **`pkg/plugin/templates/`** — built-in templates as style examples.
- **`~/.kubectl-status/templates/`** — user CRD template examples.

## Steps

### 1. Identify the resource

```bash
kubectl api-resources | grep -i <name>
```

Note the exact **Kind** string (case-sensitive — used as the template `define` name) and the `APIVERSION` column value.

### 2. Read the full CRD schema

```bash
kubectl get crd <full-crd-name> -o json | jq '.spec.versions[0].schema.openAPIV3Schema.properties'
```

Read `spec` and `status` sub-schemas **in full** — not just top-level keys. For each field, check its `description` to understand what it means. Use that meaning to decide whether it warrants inclusion, applying the output philosophy in CONVENTIONS.md.

Pay specific attention to:
- **Timestamps** — apply the date formatting rules in CONVENTIONS.md § Dates.
- **Booleans and enums** — never emit raw `true`/`false`; emit a meaningful label only when the value is operationally interesting.

### 3. Sample live instances

```bash
kubectl get <resource> -A --no-headers | head -5
kubectl get <resource> <name> -n <ns> -o json
```

Cross-reference the schema against what is actually populated. Skip status fields that are never set and spec fields always at their default.

### 4. Write the template

File: `~/.kubectl-status/templates/<Kind>.tmpl`

Copy the `define` wrapper and bookend sections from any existing template. The GVK comment (`{{- /* GVK: group/version, Kind=<Kind> */ -}}`) must follow the `gotype` comment immediately after `define`. Section order and what `status_summary_line` already covers are in CONVENTIONS.md § Section order.

All design rules are in CONVENTIONS.md. Implementation pointers:
- **Label selectors** — use `selector_with_health_summary` from `common.tmpl`; only hand-roll when custom health logic is needed.
- **Reference fields** — `HTTPRoute.tmpl` has worked examples of both single-ref and list-of-refs forms.

### 5. Verify

```bash
kubectl status <resource> <name> -n <ns>
```

Test at least two different instances to confirm optional fields appear and disappear correctly.
