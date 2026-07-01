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

Note the exact **Kind** string (case-sensitive — used as the template `define` name).

### 2. Read the full CRD schema

```bash
kubectl get crd <full-crd-name> -o json | jq '.spec.versions[0].schema.openAPIV3Schema.properties'
```

Read `spec` and `status` sub-schemas **in full** — not just top-level keys. For each field, check its `description` to understand what it means. Use that meaning to decide whether it warrants inclusion, applying the output philosophy in CONVENTIONS.md.

Pay specific attention to:
- **Timestamps** — apply the date formatting pattern in CONVENTIONS.md § Dates.
- **Booleans and enums** — never emit raw `true`/`false`; emit a meaningful label only when the value is operationally interesting.
- **Fields that are always at their default** — omit them.

### 3. Sample live instances

```bash
kubectl get <resource> -A --no-headers | head -5
kubectl get <resource> <name> -n <ns> -o json
```

Cross-reference the schema against what is actually populated. Skip status fields that are never set and spec fields always at their default.

### 4. Write the template

File: `~/.kubectl-status/templates/<Kind>.tmpl`

#### Structure

```
{{- define "<Kind>" }}
    {{- /*gotype: github.com/bergerx/kubectl-status/pkg/plugin.RenderableObject*/ -}}
    {{- /* GVK: <group>/<version>, Kind=<Kind> */ -}}
    {{- template "status_summary_line" . }}
    {{- template "kstatus_summary" . }}
    {{- template "finalizer_details_on_termination" . }}
    {{- template "observed_generation_summary" . }}
    {{- template "application_details" . }}
    ... resource-specific content ...
    {{- template "conditions_summary" . }}
    {{- template "recent_updates" . }}
    {{- template "events" . }}
    {{- template "owners" . }}
{{- end }}
```

The GVK comment (`{{- /* GVK: ... */ -}}`) must always follow the `gotype` comment. Use the exact group/version from `kubectl api-resources` output (column `APIVERSION`).

Section order and what `status_summary_line` already covers are defined in CONVENTIONS.md § Section order.

#### Output and style conventions

All design rules are in CONVENTIONS.md. Key reminders for implementation:

- **Prose over key:value** — when fields form a sentence, write prose; fall back to `Bold: cyan` for standalone fields. See CONVENTIONS.md § Prose over key:value.
- **Value highlighting** — `| cyan` on all plain values; never override `redBoldIf`, `redIf`, `colorKeyword`, `colorAgo`. Integers need `| toString` first.
- **Zero-value fields** — `{{with}}` hides `false`/`0`/`""`; use `{{if hasKey}}` when zero is operationally meaningful.
- **Dates** — `colorAgo` for past only; date-string for future (colorAgo goes negative for future timestamps).
- **Conditions** — always `conditions_summary`; never re-render manually.

#### Single-item list collapsing

See CONVENTIONS.md § Single-item list collapsing for the rule. Implementation:

```
{{- with .Spec.hostnames }}
    {{- "Hostnames" | bold | nindent 2 }}:
    {{- if eq (len .) 1 }}
        {{- " " }}{{ index . 0 | cyan }}
    {{- else }}
        {{- range . }}
            {{- . | cyan | nindent 4 }}
        {{- end }}
    {{- end }}
{{- end }}
```

Lists of structs follow the same rule — if there is one item, render its key fields inline after the title:

```
{{- with .Spec.parentRefs }}
    {{- "Parents" | bold | nindent 2 }}:
    {{- if eq (len .) 1 }}
        {{- $p := index . 0 }}
        {{- " " }}{{ printf "%s/%s" ($p.kind | default "Gateway") $p.name | cyan }}
        {{- with $p.sectionName }} [{{ . | cyan }}]{{ end }}
    {{- else }}
        {{- range . }}
            {{- printf "%s/%s" (.kind | default "Gateway") .name | cyan | nindent 4 }}
            {{- with .sectionName }} [{{ . | cyan }}]{{ end }}
        {{- end }}
    {{- end }}
{{- end }}
```

Exception: skip collapsing when items have rich sub-fields (conditions, nested refs) that always require indented lines.

#### Merging parallel spec/status lists

See CONVENTIONS.md § Merging parallel spec/status lists for the rule. Implementation — range over spec, look up matching status entry by key:

```
{{- with .Spec.listeners }}
    {{- "Listeners" | bold | nindent 2 }}:
    {{- range . }}
        {{- $spec := . }}
        {{- $status := dict }}
        {{- range $.Status.listeners }}
            {{- if eq .name $spec.name }}{{ $status = . }}{{ end }}
        {{- end }}
        {{- .name | bold | nindent 4 }}: port={{ printf "%d" (.port | int) | cyan }}/{{ .protocol | cyan }}
        {{- with .hostname }}, host={{ . | cyan }}{{ end }}
        {{- with $status.supportedKinds }}, accepts={{ range $i, $k := . }}{{ if $i }}/{{ end }}{{ $k.kind | cyan }}{{ end }}{{ end }}
        {{- if hasKey $status "attachedRoutes" }}, routes={{ $status.attachedRoutes | toString | cyan }}{{ end }}
        {{- range $status.conditions }}
            {{- $.Include "condition_summary" . | nindent 6 }}
        {{- end }}
    {{- end }}
{{- end }}
```

How to spot this pattern: a `status` list whose items share a key field (usually `name`) with a `spec` list, where status items carry only runtime fields (counts, conditions, supported kinds).

#### Label selectors

Any `LabelSelector` field must use the `labelSelector` pipe function — manually joining `matchLabels` silently drops `matchExpressions`. See CONVENTIONS.md § Label selectors for the rule.

Standard selector block (implements all three rendering modes — see CONVENTIONS.md § Rendering depth):

```
{{- with .Spec.selector }}
    {{- "Selector" | bold | nindent 2 }}: {{ . | labelSelector | cyan }}
    {{- if not ($.Config.GetBool "shallow") }}
        {{- $matchLabels := .matchLabels }}
        {{- if $.Config.GetBool "deep" }}
            {{- range $.KubeGetByLabelsMap $.Namespace "pods" $matchLabels }}
                {{- $.IncludeRenderableObject . | nindent 4 }}
            {{- end }}
        {{- else }}
            {{- $svcs := $.KubeGetServicesMatchingLabels $.Namespace $matchLabels }}
            {{- $deploys := $.KubeGetByLabelsMap $.Namespace "deployments" $matchLabels }}
            {{- $daemonsets := $.KubeGetByLabelsMap $.Namespace "daemonsets" $matchLabels }}
            {{- $statefulsets := $.KubeGetByLabelsMap $.Namespace "statefulsets" $matchLabels }}
            {{- range $svcs }}
                {{- $.Include "service_health_summary" . | nindent 4 }}
            {{- end }}
            {{- if or $deploys $daemonsets $statefulsets }}
                {{- range $deploys }}{{ $.Include "workload_health_summary" . | nindent 4 }}{{ end }}
                {{- range $daemonsets }}{{ $.Include "workload_health_summary" . | nindent 4 }}{{ end }}
                {{- range $statefulsets }}{{ $.Include "workload_health_summary" . | nindent 4 }}{{ end }}
            {{- else }}
                {{- range $.KubeGetByLabelsMap $.Namespace "pods" $matchLabels }}
                    {{- $.Include "pod_health_summary" . | nindent 4 }}
                {{- end }}
            {{- end }}
        {{- end }}
    {{- end }}
{{- end }}
```

`selector_with_health_summary` in `common.tmpl` implements this pattern — prefer it when you don't need custom health summary logic.

Note: `KubeGetByLabelsMap` queries workload metadata labels. `KubeGetServicesMatchingLabels` checks whether the service's selector is a subset of `matchLabels`.

#### Reference fields

See CONVENTIONS.md § Rendering depth for the rule. Use `$.Include "resource_ref"` (not `template`) so the result can be piped to `nindent`.

Single ref:
```
{{- with .Spec.issuerRef }}
    {{- if $.Config.GetBool "deep" }}
        {{- $obj := $.KubeGetFirst (coalesce .namespace $.Namespace) (.kind | default "Issuer") .name }}
        {{- if $obj.Object }}{{ $.IncludeRenderableObject $obj | nindent 2 }}{{ end }}
    {{- else }}
        {{- "Issuer" | bold | nindent 2 }}: {{ $.Include "resource_ref" (dict "kind" (.kind | default "Issuer") "name" .name "namespace" .namespace) }}
    {{- end }}
{{- end }}
```

List of refs:
```
{{- with .Spec.resourceRefs }}
    {{- if $.Config.GetBool "deep" }}
        {{- range . }}
            {{- $obj := $.KubeGetFirst (coalesce .namespace $.Namespace) .kind .name }}
            {{- if $obj.Object }}{{ $.IncludeRenderableObject $obj | nindent 2 }}{{ end }}
        {{- end }}
    {{- else }}
        {{- range . }}
            {{- $.Include "resource_ref" (dict "kind" .kind "name" .name "namespace" .namespace) | nindent 2 }}
        {{- end }}
    {{- end }}
{{- end }}
```

Identify ref fields by looking for objects with `kind`, `name` (and optionally `namespace`, `group`, `apiVersion`) properties, or by reading the field description.

### 5. Verify

```bash
kubectl status <resource> <name> -n <ns>
```

Test at least two different instances to confirm optional fields appear and disappear correctly.
