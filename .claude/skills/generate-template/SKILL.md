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

- **`pkg/plugin/templates/common.tmpl`** — all shared sub-templates and available functions.
- **`pkg/plugin/templates/`** — built-in templates as style examples.
- **`CONTRIBUTING.md`** — read the **General Guidelines** section (Output Contents Guidelines and Color Coding Guidelines).
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

Read `spec` and `status` sub-schemas **in full** — not just top-level keys. For each field, check its `description` to understand what it means. Use that meaning to decide whether it warrants inclusion, applying the Output Contents Guidelines under **General Guidelines** in CONTRIBUTING.md.

Pay specific attention to:
- **Timestamps** — apply the date formatting pattern below.
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

Do not reorder the shared sub-templates. Do not repeat what `status_summary_line` already shows (Kind, Name, Namespace, creation time, owner, phase).

#### Prose over key:value

When multiple related fields form a natural sentence, write prose rather than stacked labels:

```
{{- "Issued by" | nindent 2 }} {{ printf "%s/%s" .kind .name | cyan | bold }} for {{ $primary | cyan }} · stored in {{ printf "secret/%s" $.Spec.secretName | cyan }}
```

Indent supporting detail under a prose line with `nindent 4` to show grouping:

```
  Issued by ClusterIssuer/cluster-issuer for "foo" · stored in secret/foo-tls
    Org: ServiceNow
    Also valid for: foo.svc, foo.svc.cluster.local
```

Fall back to `"Label" | bold | nindent 2 }}: {{ value | cyan }}` for fields that stand alone.

#### Value highlighting

Apply `| cyan` to plain values so they are visually distinct from bold labels. Do **not** stack `cyan` on top of a semantic color function (`redBoldIf`, `redIf`, `colorKeyword`, `colorAgo`) — those must not be overridden.

`cyan` expects a string. Integer fields must be converted first:

```
{{- $statusListener.attachedRoutes | toString | cyan }}
```

#### Zero-value fields

`{{- with .someField }}` skips when the value is zero, `false`, or empty — which hides operationally meaningful zeroes (e.g. `routes=0` means nothing is attached). Use `if hasKey` instead when zero is significant:

```
{{- if hasKey $statusEntry "attachedRoutes" }}, routes={{ $statusEntry.attachedRoutes | toString | cyan }}{{ end }}
```

Use `with` only when the zero/empty case genuinely means "omit this field entirely".

#### Single-item lists

When a list field is rendered as an indented block under a bold title, check the length at render time. If there is only one item, collapse it onto the title line instead — the indented block form only pays off when there are multiple items to scan:

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

Apply this wherever a label + list would otherwise always produce a block. Lists of structs follow the same rule — if there is one item, render its key fields inline after the title rather than indenting a sub-block:

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

**Exception:** skip this collapsing when the items themselves have rich sub-fields (conditions, nested refs) that always require indented lines — collapsing the header does not help if the body still expands below it.

#### Merging parallel spec/status lists

Some resources split a single logical list across `spec` and `status` — same items, complementary fields (e.g. Gateway `spec.listeners` + `status.listeners`, both keyed by `name`). **Do not render them as two separate blocks.** Merge them by ranging over the spec list and looking up the matching status entry by key:

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

**How to spot this pattern in the schema:** look for a `status` field whose items have a `name` (or similar key) that matches item names in a `spec` list, where the status items carry only runtime fields (counts, conditions, supported kinds) and no config. When you see this, always merge — the reader should never have to cross-reference two separate blocks by name.

#### Dates

`colorAgo` uses `time.Since()`, so future dates produce a confusing negative output. For past dates use `colorAgo` with a relative hint; for future dates show only the date portion:

```
{{- $issuedDate := (split "T" .Status.issuedAt)._0 }}
{{- "Issued" | bold | nindent 2 }} {{ $issuedDate | cyan }} ({{ .Status.issuedAt | colorAgo }}{{ agoSuffix }}), expires {{ (split "T" .Status.notAfter)._0 | cyan }}
```

#### Conditions

`conditions_summary` renders `.status.conditions[]` with coloring and timestamps already applied. Do not re-render conditions manually.

#### Label selectors

Any field that is a Kubernetes `LabelSelector` (has `matchLabels` and/or `matchExpressions`) must be rendered with the `labelSelector` pipe function — it handles both selector forms and produces a `kubectl get --selector`-compatible string:

```
{{- with .Spec.selector }}
    {{- "Selector" | bold | nindent 2 }}: {{ . | labelSelector | cyan }}
{{- end }}
```

Do **not** manually join `matchLabels` keys — that silently drops `matchExpressions`.

When a `LabelSelector` targets pods (as in PDB, NetworkPolicy, etc.), show matching resource health summaries indented under the selector line. Three shared sub-templates in `common.tmpl` produce compact single-line summaries:

- **`pod_health_summary`** — `Pod/name -n ns Running, 2/2 ready`
- **`service_health_summary`** — `Service/name -n ns, 3 ready, 1 not ready`
- **`workload_health_summary`** — `Deployment/name -n ns, 2/2 ready` / `DaemonSet/name -n ns, 3/3 scheduled`

Standard selector block pattern (respects all three modes — `--shallow`, default, `--deep`):

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

Behaviour: `--shallow` → selector string only; default → services + workloads as single-line summaries (falls back to pods if no workloads match); `--deep` → full inline pod renders.

Note: `KubeGetByLabelsMap` queries metadata labels of the workload resources, not their pod template labels. This works for the common Kubernetes pattern where Deployments/DS/STS carry the same labels as their pod selector. `KubeGetServicesMatchingLabels` checks whether the service's selector is a subset of `matchLabels`, which correctly identifies services fronting the same pods.

#### Reference fields (`--deep` inline rendering)

Any field that references another Kubernetes resource by kind/name must be rendered inline when `--deep` is set. Always show the reference as `Kind/name -n namespace` using the `resource_ref` template in the default case. Use `$.Include "resource_ref"` (not `template`) so the result can be piped to `nindent`.

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

Identify ref fields in the schema by looking for objects with `kind`, `name` (and optionally `namespace`, `group`, `apiVersion`) properties, or by reading the field description.


### 5. Verify

```bash
kubectl status <resource> <name> -n <ns>
```

Test at least two different instances to confirm optional fields appear and disappear correctly.
