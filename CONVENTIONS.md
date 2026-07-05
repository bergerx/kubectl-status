# kubectl-status Conventions

Design rules that apply to all kubectl-status output and templates. Read this before writing or reviewing a template.

For the step-by-step template authoring workflow (reading CRD schemas, sampling live instances, verifying output) see [`CONTRIBUTING.md`](CONTRIBUTING.md#your-first-code-contribution).

## Output philosophy

- **Human-only output.** Don't make output parser-friendly — no stable column widths, no machine-parseable structure.
- **Compact over complete.** Compact output is the main differentiator from `kubectl describe`. Omit fields with well-known defaults (e.g. `podIP`, `hostIP`, `containerID`).
- **Readable without color.** Users share output via copy-paste, losing ANSI codes. Never rely on color alone to convey state — use text that is unambiguous in plain output. E.g. prefer `Not Ready` over coloring the word `Ready` red.
- **Transform, don't transcribe.** Raw Kubernetes field values are often not human-friendly. Prefer `Not Ready` over `Ready: false`.
- **Be opinionated.** Express status clearly. Spell out impact where it matters: a Service with no endpoints likely means an outage — say so.
- **Surface what isn't in the resource.** When status fields alone are insufficient, make additional API calls. E.g. fetch NodeMetrics and Pods when showing a Node.
- **Spec fields only when contextually necessary.** Include spec only when it sets context for understanding current status (e.g. `.spec.replicas` for a ReplicaSet). Omit pure configuration (e.g. Ingress host values).
- **Promote generic patterns.** If a convention appears across multiple resource types (e.g. `observedGeneration`, `conditions`, `replicas`), implement it in `DefaultResource` or `common.tmpl` so all resources benefit.

## Color coding

Traffic-light convention, but restrained:

| Color | Use for |
|---|---|
| regular | Healthy / nominal state |
| `green` | Explicit healthy signal from a dedicated status field — `Ready: True`, `Running`, `Active`. Do **not** use just because counts match. |
| `yellow` | Known-transient issues or bad practices — ongoing rollout, `latest` image tag. |
| `red` | Faulty states requiring attention. Use for long messages (condition `.message`). |
| `bold red` | Single words, camelCase, or PascalCase in a faulty state (condition `.reason`, a resource kind). |

For short key/value pairs in a faulty state, colorize the whole expression — not just the key or just the value. E.g. paint `readyPodCount:0` as one `red` unit.

## Template conventions

### Section order

Every template follows this fixed structure. Do not add content that duplicates what `status_summary_line` already shows (Kind, Name, Namespace, creation time, owner reference, phase).

```
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
```

The bookend sections (`kstatus_summary`, `conditions_summary`, `recent_updates`, `events`, `owners`) stay in this order. Resource-specific body sections go where they make most sense contextually — typically immediately after the content they annotate. Omit a bookend section when it adds no signal for the resource type (e.g. `kstatus_summary` always reports "Resource is always ready" for CronJob — omit it).

### Prose over key:value

When multiple related fields form a natural sentence, write prose rather than stacking `Label: value` pairs:

```
  Issued by ClusterIssuer/cluster-issuer for "foo" · stored in secret/foo-tls
    Org: ServiceNow
    Also valid for: foo.svc, foo.svc.cluster.local
```

Reserve `**Bold label**: cyan value` for fields that genuinely stand alone and don't connect to adjacent fields.

### Value highlighting

Apply `| cyan` to plain values so they are visually distinct from bold labels. Never stack `cyan` on top of a semantic color function — `redBoldIf`, `redIf`, `colorKeyword`, and `colorAgo` must not be overridden.

`cyan` expects a string; convert integers first: `{{ .count | toString | cyan }}`.

### Zero-value fields

`{{- with .field }}` skips when the value is `false`, `0`, or `""`. This hides operationally meaningful zeroes — `routes=0` means nothing is attached and is worth showing. Use `if hasKey` when zero is significant:

```
{{- if hasKey $status "attachedRoutes" }}, routes={{ $status.attachedRoutes | toString | cyan }}{{ end }}
```

Use `with` only when the zero/empty case genuinely means "omit this field entirely".

### Single-item list collapsing

An indented block for a single item wastes vertical space. When rendering a labeled list, check the length: if there is exactly one item, collapse it onto the title line. The block form only pays off with multiple items to scan.

Exception: when items themselves have rich sub-fields (conditions, nested refs) that always need indented lines, collapsing the header does not help.

### Merging parallel spec/status lists

Some resources split a single logical list across `spec` and `status` — same items keyed by name, with complementary fields (e.g. `spec.listeners` carries port/protocol/hostname; `status.listeners` carries conditions and attached route counts). **Never render these as two separate blocks** — that forces the reader to cross-reference by name.

Spot the pattern: a `status` list whose items have a key field (usually `name`) that matches items in a `spec` list, where status items carry only runtime fields. When you see it, range over spec and look up the matching status entry by key.

### Dates

`colorAgo` uses `time.Since()` — it produces a confusing negative value for future timestamps. Use `colorAgo` only for past dates. For future dates (expiry, scheduled time), show only the date portion (`(split "T" .timestamp)._0`).

### Conditions

Use `conditions_summary` for `.status.conditions[]`. It applies coloring and relative timestamps. Never re-render conditions manually.

### Label selectors

Any field that is a Kubernetes `LabelSelector` (`matchLabels` and/or `matchExpressions`) must be rendered with the `labelSelector` pipe function. Manually joining `matchLabels` keys silently drops `matchExpressions`.

When a selector targets pods, show matching resource health summaries indented under the selector line — see [Rendering depth](#rendering-depth) below.

### Rendering depth

Any template section that fetches related resources must respect the three rendering modes:

**`--shallow`** — skip the section entirely. Some Go helpers already return an empty slice in shallow mode (e.g. `KubeGetIngressesMatchingService`); for label-based lookups, gate explicitly in the template with `if not ($.Config.GetBool "shallow")`.

Note: `--local` runs without a live cluster, so `KubeGetFirst` calls fail to find anything there too — templates don't need to check for `--local` explicitly, they just need to handle the "not found" case (typically falling back to `resource_ref`), which the `--shallow` handling above already requires.

**default** — compact single-line summaries using shared sub-templates from `common.tmpl`:

| Sub-template | Example output |
|---|---|
| `pod_health_summary` | `Pod/name -n ns, 2/2 ready` |
| `service_health_summary` | `Service/name -n ns, 3 ready, 1 not ready` |
| `workload_health_summary` | `Deployment/name -n ns, 2/2 ready` |
| `job_health_summary` | `Job/name -n ns, Active, started 5m ago` |

**`--deep`** — full inline render with `$.IncludeRenderableObject . | nindent 4`.

For **label selectors**, `selector_with_health_summary` in `common.tmpl` implements all three modes automatically — prefer it over hand-rolling the same logic.

For **reference fields** (spec fields pointing to another resource by kind/name), show `Kind/name -n ns` via the `resource_ref` sub-template in the default case, and `$.IncludeRenderableObject` in deep mode. `HTTPRoute.tmpl` has worked examples of both single-ref and list-of-refs forms.

### Go template `and`/`or` do not short-circuit

Unlike most languages, Go templates evaluate **all** arguments to `and` and `or` before applying the logic. `{{ if and .A (someFunc .A.B) }}` panics when `.A` is nil because `.A.B` is always evaluated. Use nested `with`/`if` blocks for any chained field access that could be nil:

```
{{- with .Status.lastSuccessfulTime }}
    {{- if lt . $threshold }}
```

Never rely on `and` to guard a nil dereference.

### Omit spec fields at their Kubernetes default

Show a spec field only when it deviates from the Kubernetes-defined default. Emitting the default adds noise for the vast majority of users running standard configurations. Common defaults to suppress: `RollingUpdate` (Deployment strategy), `Allow` (CronJob concurrencyPolicy), `/metrics` and `HTTP` (ServiceMonitor endpoint path/scheme), `NonIndexed` (Job completionMode), `TerminatingOrFailed` (Job podReplacementPolicy).

Use `ne (.field | default "DefaultValue") "DefaultValue"` to gate the output.

