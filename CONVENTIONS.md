# kubectl-status Conventions

Design rules that apply to all kubectl-status output and templates. Read this before writing or reviewing a template.

For the step-by-step template authoring workflow (reading CRD schemas, sampling live instances, verifying output) see [`CONTRIBUTING.md`](CONTRIBUTING.md#your-first-code-contribution). For the full list of `{{define}}` names and functions a `~/.kubectl-status/templates/<Kind>.tmpl` override can safely call — as opposed to internal helpers that may change without notice — see [`TEMPLATE-API.md`](TEMPLATE-API.md).

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

### Go function vs. `{{define}}` partial

A shared snippet with a small, fixed set of parameters and no control flow of its own — pure formatting, like `resource_ref` (`dict "kind" "name" "namespace"(opt) "callerNamespace"(opt) "nameSuffix"(opt)` → a colored `Kind/name -n ns` string) — belongs in a Go function registered in the funcMap (grouped into the topic file its neighbors already live in, e.g. `color.go`, `format.go`), not a `{{define}}` block taking an untyped `dict`. A Go signature is compiler-checked (wrong argument count/order is a build failure, not a blank field at render time) and gets ordinary unit test coverage in that file's `_test.go`, the same way `colorPercent` already does.

A snippet stays a `{{define}}` partial when the template's own control flow (`range`/`with`/`if`) is doing real work — composing sections, iterating a list, deciding what to render at all — rather than just formatting fixed inputs into a string. `managed_resource_line` and `deep_render_ref` are this kind: they dispatch and compose, they don't just format.

When a partial name is part of the documented stable surface (see TEMPLATE-API.md) and gets converted to a Go function, keep a thin `{{define}}` wrapper under the original name that delegates to the new function — external user templates calling `{{template "name" (dict ...)}}` directly must keep working unchanged.

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
{{- if $status | hasKey "attachedRoutes" }}, routes={{ $status.attachedRoutes | toString | cyan }}{{ end }}
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

**default** — compact single-line summaries, one `"<Kind>.summary"` template per Kind (defined
alongside that Kind's own `<Kind>.tmpl`), reached via `resource_health_summary`/`generic_health_summary`
in `common.tmpl` — see [TEMPLATE-API.md's Health-summary family](TEMPLATE-API.md#health-summary-family)
for the full list and the discovery mechanism:

| `<Kind>.summary` | Example output |
|---|---|
| `Pod.summary` | `Pod/name -n ns, 2/2 ready` |
| `Service.summary` | `Service/name -n ns, 3 ready, 1 not ready` |
| `Deployment.summary` (also Stateful/DaemonSet/ReplicaSet) | `Deployment/name -n ns, 2/2 ready` |
| `Job.summary` | `Job/name -n ns, Active, started 5m ago` |

**`--deep`** — full inline render with `$.IncludeRenderableObject . | nindent 4`.

For **matched Pods** specifically, a Pod whose own kstatus disagrees with `Current` (crash-looping, unschedulable, stuck Pending, ...) gets the full inline render even outside `--deep` — see `.Problematic` in [TEMPLATE-API.md](TEMPLATE-API.md). A Pod that's already flagged as the workload's problem shouldn't need a second `--deep` invocation just to see what's wrong with it. Gate this with `if or ($.Config.GetBool "deep") .Problematic` around the same `IncludeRenderableObject`/`"Pod.summary"` branch used for `--deep`.

For **label selectors**, `selector_with_health_summary` in `workloads_common.tmpl` implements all three modes automatically, including the problematic-Pod inlining above — prefer it over hand-rolling the same logic.

For **reference fields** (spec fields pointing to another resource by kind/name), show `Kind/name -n ns` via the `resource_ref` sub-template in the default case, and `$.IncludeRenderableObject` in deep mode. `HTTPRoute.tmpl` has worked examples of both single-ref and list-of-refs forms.

Where the ref sits on a line of its own, deep mode replaces it with the inlined object (`ExternalSecret.tmpl`, `HTTPRoute.tmpl`). Where it's named mid-sentence — `Applies ./x from GitRepository/y into namespace z` — substituting a multi-line block would destroy the sentence, so pair the `resource_ref` line with `deep_render_ref`, which appends the inlined object underneath and renders nothing in the other modes:

```
{{- "Applies" | bold | nindent 2 }} from {{ $.Include "resource_ref" (dict "kind" .kind "name" .name) }}
{{- $.Include "deep_render_ref" (dict "ctx" $ "kind" .kind "name" .name) }}
```

Either shape is fine; the requirement is that the ref itself stays on screen in all three modes. `deep_render_ref` needs no `--shallow`/`--local` check of its own — `KubeGetFirst` already returns an empty object whenever `LiveQueriesDisabled()` is true.

For a **list of resources another object manages** — a Kustomization's `status.inventory`, a Crossplane XR's `resourceRefs`, the objects in a Helm release manifest — use `managed_resource_line`, which implements all three modes plus the not-found case in one call:

```
{{- $.Include "managed_resource_line" (dict "ctx" $ "kind" $kind "name" $name "namespace" $ns "group" $group) | nindent 4 }}
```

It renders the full inline object under `--deep`, a per-kind health summary by default (via `resource_health_summary`, so a mixed-kind list needs no dispatch of its own), and a bare `resource_ref` when the object can't be fetched. A reference with nothing behind it is additionally marked `missing`, since naming a resource you manage is a claim that it exists; that marking is suppressed when live queries are off, where *every* lookup comes back empty for an unrelated reason. Callers apply their own `nindent` — the helper emits no leading indentation, so the same call works at any depth.

### Always pass the API group with a reference

A `Kind` is not a unique name: `Secret` exists in the core group *and* in `keyvault.azure.m.upbound.io`, `Gateway` in both `gateway.networking.k8s.io` and `networking.istio.io`. `KubeGet`/`KubeGetFirst` resolve an unqualified kind through discovery across every group and silently pick whichever wins the tie — which is how a Crossplane XR composing a provider `Secret` ended up rendering an unrelated core Secret's contents under the right name.

So wherever a reference carries an `apiVersion` or a `group` (an `ownerReference`, a Crossplane `resourceRefs` entry, a Flux inventory id's third segment, a Gateway API `backendRef`, a Helm release manifest document), pass it on:

- to the **lookup**, via `qualifyKind` — `$.KubeGetFirst $ns (qualifyKind .kind .apiVersion) .name` — or the `"group"` key of `managed_resource_line`/`deep_render_ref`.
- to the **display**, via the `"group"` key of `resource_ref`. Only a group Kubernetes doesn't serve itself is actually printed (`Secret.keyvault.azure.m.upbound.io/creds`); `Deployment.apps` is noise and is suppressed, so passing the group is always safe.

Where the group is a fixed constant the template already knows (Gatekeeper's `constraints.gatekeeper.sh`, Istio's `networking.istio.io`), pass the literal. The two places that deliberately stay unqualified are an object's own header line in `status_summary_line` (nothing to disambiguate it against — the user asked for it by name) and a Flux `spec.dependsOn` ref (always the same Kind as the object declaring it, which the header line just showed).

### Go template `and`/`or` do not short-circuit

Unlike most languages, Go templates evaluate **all** arguments to `and` and `or` before applying the logic. `{{ if and .A (someFunc .A.B) }}` panics when `.A` is nil because `.A.B` is always evaluated. Use nested `with`/`if` blocks for any chained field access that could be nil:

```
{{- with .Status.lastSuccessfulTime }}
    {{- if lt . $threshold }}
```

Never rely on `and` to guard a nil dereference.

### Never trust a field to be present, however required the schema says it is

A rendering error aborts the **entire object**, not the line that caused it — `Include` propagates the error up, so you lose the conditions, events, and everything else for exactly the resource someone was trying to debug. A template must degrade one line, never the whole render.

`required:` in a CRD schema is not a guarantee at render time:

- `--local` / `-f file.yaml` renders hand-written manifests that never passed API-server validation.
- CRD schemas change between operator versions; an object written by an older controller can be missing a field the current schema marks required, and a `status` block is written asynchronously — an object observed mid-reconcile has a partial one.
- Third-party CRDs are the common case here, and their schemas are frequently looser than their docs imply.

The trap is that these paths are the ones a template is *least* likely to be exercised against, so the crash surfaces in the field.

Color functions are the usual detonator — `cyan` and friends reject a nil with `invalid value; expected string`:

```
{{- .kind | cyan }}                    {{- /* aborts the object's render when kind is absent */}}
{{- .kind | default "" | cyan }}       {{- /* degrades to an empty segment */}}
```

`toString` is the other one: it turns a missing field into a literal `<nil>` on screen rather than an error, which is worse — it looks like real data.

This is not a licence to wrap every field in `default`. The `with`/`if` guards the conventions already call for cover the overwhelming majority of cases, and they read better than a wall of defaulted pipelines — `{{- with .Spec.url }}url: {{ . | cyan }}{{ end }}` needs nothing further. Reach for `default` only in the three spots a guard can't reach:

- **Values passed to a shared sub-template**, which can't guard on behalf of its caller. A single unguarded `cyan` in `common.tmpl` is a latent crash for all ~50 call sites, so helpers default their own inputs.
- **Sub-fields of an item inside a `range`**, where wrapping each one in its own `with` would bury the line. When several optional fields concatenate there, track the separators rather than hardcoding them so any subset composes — `storageclass_summary` is the reference for the `$needsComma` pattern.
- **Anything reaching `toString`**, which has no failure mode that looks like a failure.

Everywhere else, let the test tell you. Render a stripped-down manifest with `--local` where optional fields are absent and the "required" ones are missing or empty, and guard what actually breaks — see CONTRIBUTING.md § Working with Test Artifacts. Guessing at it up front produces noise; the render either aborts or prints `<nil>`, and both are obvious.

The same proportionality applies to parsing. `colorAgo` accepts exactly one layout and silently returns the zero time for anything else, rendering an age that is wrong by two millennia but looks plausible. For a Kubernetes-managed timestamp that's fine — the API server normalizes them. For a value a human or a CLI writes, such as the `reconcile.fluxcd.io/requestedAt` annotation, print it raw instead of inventing a guard.

### Omit spec fields at their Kubernetes default

Show a spec field only when it deviates from the Kubernetes-defined default. Emitting the default adds noise for the vast majority of users running standard configurations. Common defaults to suppress: `RollingUpdate` (Deployment strategy), `Allow` (CronJob concurrencyPolicy), `/metrics` and `HTTP` (ServiceMonitor endpoint path/scheme), `NonIndexed` (Job completionMode), `TerminatingOrFailed` (Job podReplacementPolicy).

Use `ne (.field | default "DefaultValue") "DefaultValue"` to gate the output.

