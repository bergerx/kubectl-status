# Template API

`kubectl-status` renders every Kubernetes object through Go [`text/template`](https://pkg.go.dev/text/template).
A user can drop a file into `~/.kubectl-status/templates/<Kind>.tmpl` (see
[CONTRIBUTING.md](CONTRIBUTING.md#claude-code-integration)) to add or override the rendering for a
Kind. That override file can call back into names this project defines — `{{ template "resource_ref" ... }}`,
`{{ .KubeGetFirst ... }}`, `{{ .Status.foo | colorAgo }}` — and those names are a real integration
surface, arguably the main one: [CONVENTIONS.md](CONVENTIONS.md) explicitly rules out stdout as a
parseable interface ("Human-only output"), which leaves the template mechanism as the only thing an
external integration can depend on.

This document is the contract for that surface. It enumerates every name a user template is safe to
depend on:

- the `{{define "..."}}` blocks in `pkg/plugin/templates/*.tmpl` — both the per-Kind entry points and
  the shared partials built for reuse across them
- the `template.FuncMap` functions registered in `pkg/plugin/template_functions_static.go`
- the exported methods and fields on `RenderableObject` (the "." context every template renders
  against) — defined across `pkg/plugin/renderable.go` and `pkg/plugin/template_functions_dynamic.go`

**Anything not listed here is internal and may be renamed, restructured, or removed without notice,
including a `{{define}}` block that happens to be syntactically callable today.** Only exported Go
identifiers are reachable from a template in the first place — `text/template` resolves methods and
struct fields through reflection, which simply cannot see an unexported (lowercase) name — so every
`RenderableObject` method below is capitalized by construction; lowercase helpers such as
`newRenderableObject` or `executeTemplate` are not part of this surface and are omitted rather than
listed as "internal" for that reason.

See [issue #812](https://github.com/bergerx/kubectl-status/issues/812) for the discussion behind this
document, and the [Versioning policy](#versioning-policy) section below for what happens when the stable
list below has to change. Enforcing this boundary in code (making internal helpers actually unreachable
from a user template) is tracked separately and is not what this document does — this is the
enumeration the enforcement work builds against.

## Table of contents

- [Kind templates](#kind-templates)
- [Shared render helpers (stable)](#shared-render-helpers-stable)
- [The user override point: `custom_application_details`](#the-user-override-point-custom_application_details)
- [FuncMap functions (stable)](#funcmap-functions-stable)
- [`RenderableObject` methods and fields (stable)](#renderableobject-methods-and-fields-stable)
- [Everything else is internal](#everything-else-is-internal)
- [Borderline cases worth a second look before #809](#borderline-cases-worth-a-second-look-before-809)
- [Versioning policy](#versioning-policy)

## Kind templates

Each Kubernetes Kind kubectl-status has a dedicated view for gets one `{{define "<Kind>"}}` block in its
own `pkg/plugin/templates/<Kind>.tmpl` file. `findTemplateName` (`pkg/plugin/render_engine.go`) picks
which define to execute for a given object:

1. `"<Kind>.<group>"` if a template registered under that exact name exists (lets two different API
   groups both define a Kind of the same name — e.g. a future Gateway API vs. Istio `Gateway` collision
   — resolve to different templates). No built-in template currently uses this form; it exists for that
   case, not for any template shipped today.
2. the bare `"<Kind>"` name, which is what every shipped template (and `~/.kubectl-status/templates/<Kind>.tmpl`)
   registers under.
3. `"DefaultResource"` (defined at the top of `common.tmpl`) when neither of the above exists — the
   generic fallback view used for any Kind without a dedicated template, including arbitrary CRDs.

Dropping `~/.kubectl-status/templates/<Kind>.tmpl` on disk defines (or, for a Kind that also ships a
built-in template, **redefines** — Go's `template.ParseGlob` lets a later parse overwrite an earlier
`{{define}}` of the same name) that Kind's top-level template. This is the documented, intended
extension mechanism (see [CONTRIBUTING.md](CONTRIBUTING.md#claude-code-integration) and the
`/generate-template` skill) and every name below is part of the stable contract by definition — the
whole point of a `<Kind>.tmpl` file is to be that Kind's template.

The 62 Kind names currently shipped (plus `DefaultResource`):

BackendTLSPolicy, Certificate, CertificateRequest, CertificateSigningRequest, ClusterPolicyReport,
Composition, ConfigMap, CronJob, CustomResourceDefinition, DaemonSet, Deployment, DestinationRule,
Event, ExternalSecret, FlowSchema, GRPCRoute, Gateway, GatewayClass, HTTPRoute, HelmRelease,
HorizontalPodAutoscaler, Ingress, Issuer, Job, K8sRequiredLabels, Kustomization, Lease, LimitRange,
ListenerSet, MutatingWebhookConfiguration, Namespace, Node, NodeClaim, NodePool, PersistentVolume,
PersistentVolumeClaim, Pod, PodDisruptionBudget, PodMonitor, PolicyReport, PriorityLevelConfiguration,
PrometheusRule, ReferenceGrant, ReplicaSet, ResourceQuota, Secret, SecretStore, Service, ServiceMonitor,
StatefulSet, StorageClass, TCPRoute, TLSRoute, UDPRoute, ValidatingAdmissionPolicy,
ValidatingAdmissionPolicyBinding, ValidatingWebhookConfiguration, VerticalPodAutoscaler, VirtualService,
VolumeAttachment, VolumeSnapshot, VolumeSnapshotContent, **DefaultResource**.

Eleven of these are also invoked textually as `{{ $.Include "<Kind>" $obj }}` by another built-in
template to inline-render a nested object under `--deep` (e.g. `matching_services` calls
`Include "Service"`, `storageclass_summary` calls `Include "StorageClass"`) — still Category A, and
still safe for a user override to replace, since `Include`/`$.Include` always resolves the name
currently registered in the template set, override or not.

Sixteen of these also pair their `<Kind>.tmpl` with a `"<Kind>.summary"` define (or
`"<Kind>.<group>.summary"` for a Kind name that collides across API groups) — the compact
one-line view `resource_health_summary` dispatches to for that Kind, found by the identical
lookup used above rather than a hand-maintained list. See the
[Health-summary family](#health-summary-family) section below for the full set and the
discovery mechanism; the same "override applies wherever it's referenced" guarantee holds.

## Shared render helpers (stable)

These are `{{define}}` blocks — almost all in `pkg/plugin/templates/common.tmpl`, three in
`gatekeeper_constraint_common.tmpl`/`policy_report_common.tmpl`/`Event.tmpl`/`Ingress.tmpl` as noted —
that are called from **multiple, unrelated Kind templates** rather than being private to one file, or
are the specific building blocks [CONVENTIONS.md](CONVENTIONS.md) already tells template authors to
reuse. A `<Kind>.tmpl` override is expected to call these by name; their argument shape (usually a
`dict` with specific keys) and behavior are the contract. All of them are called with `{{ template "name" arg }}`
or `` {{ $.Include "name" arg }} `` (`Include` is a `RenderableObject` method documented below; the two
call forms are interchangeable except `Include` also returns an `error` a caller can check).

### Per-object skeleton

Called from nearly every Kind template, in the fixed order documented in
[CONVENTIONS.md § Section order](CONVENTIONS.md#section-order). All take the `RenderableObject` being
rendered as `.` (no `dict`).

| Name | Renders |
|---|---|
| `status_summary_line` | The one-line header: `Kind/name -n ns, created <age>, by <owner>, gen:N<, started after ...><, phase>`. Every template's first line. |
| `kstatus_summary` | `sigs.k8s.io/cli-utils` `kstatus.Compute` result — overall `Status`/`Message` plus each contributing `Condition`. |
| `finalizer_details_on_termination` | `metadata.finalizers` when the object has a `deletionTimestamp` — i.e. what's blocking a delete in progress. |
| `observed_generation_summary` | A warning when `status.observedGeneration != metadata.generation` (controller hasn't reconciled the latest spec yet). |
| `application_details` | "Managed by ..." derived from common Helm/addon-manager/`app.kubernetes.io/*` labels and annotations, gated behind `--include-application-details`. Always chains into `flux_object_management` (Flux-stamped labels/annotations, ungated) and [`custom_application_details`](#the-user-override-point-custom_application_details). |
| `conditions_summary` | Every entry in `.StatusConditions`, via `condition_summary` per item (skips empty placeholder entries). |
| `condition_summary` | One condition, `.` is the condition map: colored `Type:Status`, `reason`, `message`, and the relevant timestamp. |
| `recent_updates` | `metadata.managedFields`, sorted by time, gated behind `--include-managed-fields`. |
| `events` | This object's Events (via `.KubeGetEvents`), gated behind `--include-events`; renders each item via `event` (defined in `Event.tmpl`, see below). |
| `owners` | Resolved `ownerReferences`: the owning objects inlined (gated behind `--include-owners`), plus an `Orphan` line for any reference whose target no longer exists. |

### Reference / deep-render primitives

The building blocks [CONVENTIONS.md § Rendering depth](CONVENTIONS.md#rendering-depth) documents for
implementing the `--shallow`/default/`--deep` pattern.

- **`resource_ref`** — `dict "kind" "name" "namespace"(opt) "callerNamespace"(opt) "nameSuffix"(opt)`.
  Renders `Kind/name`, `-n namespace` appended unless `namespace == callerNamespace`, an optional
  suffix glued onto `name` (e.g. a port) so it can't be misread as qualifying the namespace instead.
  `kind`/`name` are defended with `default ""` so a nil ref field degrades to an empty segment instead
  of aborting the whole object's render.
- **`deep_render_ref`** — `dict "ctx" "kind" "name" "namespace"(opt, defaults to ctx's) "indent"(opt, default 4)`.
  Renders nothing unless `--deep`; when `--deep`, fetches the referenced object and inlines it,
  indented. Pairs with a `resource_ref` call on the line above so the reference itself survives in
  every mode — see the worked example in
  [CONVENTIONS.md](CONVENTIONS.md#rendering-depth). Silent (no fetch attempted) whenever the object
  can't be found, which covers `--shallow`/`--local` without an extra check.
- **`managed_resource_line`** — `dict "ctx" "kind" "name" "namespace"(opt, defaults to ctx's)`. One
  line for one object some other object claims to manage (a Kustomization's `status.inventory`, a
  Crossplane XR's `resourceRefs`, a Helm release manifest entry): the full inline render under
  `--deep`, a compact per-kind health summary by default (via `resource_health_summary`), or a bare
  `resource_ref` marked `missing` when the object can't be found (suppressed under
  `--shallow`/`--local`, where every lookup is empty for an unrelated reason). Emits no indentation of
  its own — callers pipe through `nindent`.
- **`event`** *(defined in `Event.tmpl`)* — `.` is one `Event` object's fields (a `status.items[]`
  entry, or the object itself when rendering a standalone Event). Renders the one-line
  `source, Reason message, involving Kind/name[field] (in ns), <age> (xN over ...)` form `events` uses
  per item. Callable directly if a template wants exactly one Event line without the surrounding
  "Events:" block/`--include-events` gate.

### Health-summary family

One-line, kind-specific compact summaries. Each Kind that wants one defines a
`"<Kind>.summary"` template alongside its own `<Kind>.tmpl` — the same file, same discovery
mechanism as the Kind templates above, just with a `.summary` suffix instead of a bare Kind
name (and, for a Kind name that collides across API groups, `"<Kind>.<group>.summary"` mirrors
`"<Kind>.<group>"`). Because it's a lookup rather than a hand-maintained dispatch table, a
custom template for a CRD kind — or a user override of a built-in one — picks up its own
`"<Kind>.summary"` automatically; see `RenderableObject.HealthSummary`
(`pkg/plugin/renderable.go`) and `templateSet.findSummaryTemplateName`
(`pkg/plugin/render_engine.go`). Every `"<Kind>.summary"` template takes the same
`dict "obj" "callerNamespace"(opt)` shape and starts with `resource_ref` then appends
kind-specific health signals, finishing with up to two shared partials (`common.tmpl`), both
taking the object itself rather than a dict:

- `kstatus_if_abnormal` — kstatus, but only when its `Status` disagrees with `Current`. Only
  called by the 9 kinds whose own summary doesn't already show kstatus unconditionally
  (`generic_health_summary`/`Ingress.summary`/`route_health_summary` already do, so they skip
  this). Gated rather than unconditional because kstatus doesn't know every built-in kind —
  `HorizontalPodAutoscaler`/`VerticalPodAutoscaler` aren't in its list and carry no `Ready`
  condition, so it would otherwise always claim `Current` regardless of their real state.
- `other_unhealthy_conditions` — the object's own `Ready` condition, if it has one, always shown
  first and color-coded green/red/yellow by its own status rather than gated on health (repeated
  here even when a caller's own primary-status line or `kstatus_if_abnormal` already reflects it
  in translated form), followed by every other `status.conditions` entry `isStatusConditionHealthy`
  calls unhealthy, by reason (falling back to message, then `Type:Status`) — so a sibling
  condition like Crossplane's `Synced` or an operator's own `Degraded`/`*Error` type isn't
  invisible just because the kind's own headline status above it looks fine.

| Name | Covers |
|---|---|
| `Pod.summary` (`Pod.tmpl`) | `ready` count, restart count, waiting reasons, plus compact Node-problem and NetworkPolicy-restriction flags (`pod_node_problem_flags`/`pod_network_policy_flags`, internal). |
| `Service.summary` (`Service.tmpl`) | A Service: type, endpoint ready/not-ready counts, ports. |
| `Deployment.summary`/`StatefulSet.summary`/`DaemonSet.summary`/`ReplicaSet.summary` (each Kind's own `.tmpl`, all four thin wrappers around the shared `workload_health_summary` in `workloads_common.tmpl`) | ready/desired count plus rollout-in-progress flag. |
| `Job.summary` (`Job.tmpl`) | A Job: active/succeeded/failed counts, run duration. |
| `Ingress.summary` (`Ingress.tmpl`) | An Ingress: rule hosts, LoadBalancer address, kstatus. |
| `HTTPRoute.summary`/`GRPCRoute.summary`/`TCPRoute.summary`/`UDPRoute.summary`/`TLSRoute.summary` (each Kind's own `.tmpl`, all five thin wrappers around the shared `route_health_summary` in `gatewayapi_common.tmpl`) | hostnames, kstatus. |
| `PodDisruptionBudget.summary` (`PodDisruptionBudget.tmpl`) | A PodDisruptionBudget: min/max available, current budget state. |
| `HorizontalPodAutoscaler.summary` (`HorizontalPodAutoscaler.tmpl`) | A HorizontalPodAutoscaler: current/desired vs. min-max range. |
| `VerticalPodAutoscaler.summary` (`VerticalPodAutoscaler.tmpl`) | A VerticalPodAutoscaler: update mode, per-container target recommendation. |
| `generic_health_summary` | `dict "obj" "callerNamespace"(opt)`. Fallback for any kind without its own `"<Kind>.summary"` — kstatus, a bare `status.ready` bool, observedGeneration mismatch. Reasonable to call directly for a mixed list of your own CRD kinds. |
| `resource_health_summary` | `dict "obj" "callerNamespace"(opt)`. Dispatches to `obj`'s own `"<Kind>.summary"`/`"<Kind>.<group>.summary"` if one is defined (via `RenderableObject.HealthSummary`), falling back to `generic_health_summary`. This is what `managed_resource_line` calls internally; call it directly when you have a mixed-kind list and don't want to dispatch yourself. |

`selector_with_health_summary` (`.` = the object with `.Spec.selector`) renders the `Selector: ...`
line plus each matching Pod via `Pod.summary` (or full inline under `--deep`) — the pattern
[CONVENTIONS.md](CONVENTIONS.md#label-selectors) calls out by name for any `LabelSelector` field
targeting Pods.

Every `"<Kind>.summary"` template is covered by a static check
(`TestSummaryTemplatesRenderOneLine` in `templates_common_test.go`) that renders it and fails if
the output contains a newline — the family's whole point is a summary that stays on one line
wherever a list renders one entry per row (`managed_resource_line`, `matching_services`,
`crossplane_composed_resources`, ...). A new `"<Kind>.summary"` is picked up automatically; add
a `summaryTemplateFixtures` entry only if the default empty-object fixture isn't enough for it
to render without error.

### Workload-family bundle

Built for the controllers that own a Pod template (Deployment, StatefulSet, DaemonSet, ReplicaSet,
Job, CronJob) and reusable as-is by a custom controller CRD shaped the same way:

- **`matching_workload_resources`** — `dict "ctx" "namespace" "labels" "scalable"(bool) "vpaTargetable"(bool) "serviceExpected"(bool)`.
  Renders every one of the pieces below for the workload's Pod-template labels in one call.
- **`matching_services`** — `dict "ctx" "svcs"` (caller-resolved `[]RenderableObject`, since Pods and
  workloads discover their Services differently).
- **`matching_pdbs`** — `dict "ctx" "namespace" "labels"`. PodDisruptionBudgets matching these Pod
  labels, plus a conflict warning if more than one matches (K8s doesn't support a Pod covered by two).
- **`matching_network_policies`** / **`matching_cilium_network_policies`** / **`matching_calico_network_policies`** —
  `dict "ctx" "namespace" "labels"` each. The NetworkPolicy/CiliumNetworkPolicy+CiliumClusterwideNetworkPolicy/Calico
  NetworkPolicy+GlobalNetworkPolicy families restricting these Pod labels.
- **`service_account_summary`** — `dict "ctx" "namespace" "serviceAccountName"`. The Pod template's
  ServiceAccount: annotations (cloud IAM binding), `automountServiceAccountToken`, `imagePullSecrets`.
  Suppressed for the ubiquitous unmodified `default` SA.
- **`suspended`** / **`replicas_status`** — `.` = the object. `suspended`: a red note when
  `spec.replicas == 0`. `replicas_status`: `desired:N, existing:N, ready:N, ...` with any field that
  doesn't match `spec.replicas` bolded red.
- **`rollout_diffs_flag_help`** — `.` = the object. A one-line pointer to `--include-rollout-diffs`
  when it isn't already set.
- **`quota_headroom`** — `.` = a Deployment or StatefulSet. Warns when the namespace's ResourceQuota
  can't fit the Pods a stuck-looking rollout still needs to create.
- **`match_resources_summary`** — `.` = a `MatchResources` object (`matchPolicy`/`namespaceSelector`/
  `objectSelector`/`resourceRules`/`excludeResourceRules`). Shared by
  `ValidatingAdmissionPolicy.spec.matchConstraints` and
  `ValidatingAdmissionPolicyBinding.spec.matchResources`.

### Misc. cross-Kind families

- **`certificate_validity_line`** — `dict "cert"` (an entry shaped like the output of
  `parseTLSSecretCertificate`/`certificatesInSecret`/`certificatesInConfigMap`/`certificateInCSR`:
  `NotBefore`, `NotAfter`, `Expired`). Renders the `Valid .../Expired ...` line.
- **`storageclass_summary`** — `dict "ctx" "name" "provisionerShown"(opt bool) "warnMissing"(opt bool)`.
  Resolves a `storageClassName` to its StorageClass and surfaces `volumeBindingMode`,
  `allowVolumeExpansion`, `allowedTopologies` when any is non-default.
- **`load_balancer_ingress`** *(defined in `Ingress.tmpl`)* — `.` = the object with
  `.Status.loadBalancer.ingress`. Shared between Ingress and Service (`type: LoadBalancer`), since
  both carry the same `status.loadBalancer.ingress` shape.
- **`flux_reconciliation`** — `.` = a Flux HelmRelease/Kustomization/source object. `spec.interval`/
  `spec.suspend`/pending-reconcile-request handling common to every Flux toolkit CRD.
- **`flux_depends_on`** — `.` = same. Renders `spec.dependsOn` refs plus their deep-render.
- **`istio_export_to`** / **`istio_validation_messages`** — `.` = a DestinationRule or VirtualService.
  `spec.exportTo` namespace visibility; istiod's `status.validationMessages` analysis findings.
- **`gatekeeper_constraint_match_and_enforcement`** / **`gatekeeper_constraint_audit_status`** — `.` = any
  Gatekeeper Constraint object (`constraints.gatekeeper.sh/*`: `K8sRequiredLabels` and every other
  dynamically-generated Constraint Kind share this shape). `spec.match`/enforcement mode; audit
  timestamp/violations/per-replica sync status. The pattern any custom Gatekeeper Constraint Kind
  template (see `K8sRequiredLabels.tmpl`) is expected to call.
- **`policy_report_body`** *(defined in `policy_report_common.tmpl`)* — `.` = a PolicyReport or
  ClusterPolicyReport (`wgpolicyk8s.io`, identical schema at the object root). `scope`/`scopeSelector`
  plus pass/fail/warn/error/skip counts.

## The user override point: `custom_application_details`

`custom_application_details` (`common.tmpl`) is a deliberate no-op:

```
{{- define "custom_application_details" }}
    {{- /* Can be overridden through ~/.kubectl-status/templates/*.tmpl files to inject user-specific data */ -}}
{{- end -}}
```

`application_details` — which nearly every Kind template calls — calls this unconditionally as its
last step. It exists purely so a `~/.kubectl-status/templates/*.tmpl` file can redefine
`custom_application_details` once (in any `.tmpl` file, not necessarily named after a Kind — anywhere
`ParseGlob` picks up) and have that logic run for **every** object kubectl-status renders, without
touching any Kind's own template. This is the one name in this document whose entire purpose is to be
*redefined*, not called — it is the officially sanctioned global hook, and it stays stable for exactly
that reason.

## FuncMap functions (stable)

Registered in `func (cfg *RenderConfig) funcMap()`, `pkg/plugin/template_functions_static.go`, and
merged with the [go-sprout `sprig`-compatible](https://github.com/go-sprout/sprout) function set (minus
`env`/`expandEnv`/`expandenv`, deliberately removed so a stray template can't read process environment
variables). Sprig/sprout functions themselves (`default`, `dict`, `list`, `hasKey`, `nindent`, ...) are
a third-party contract, not this project's — see the
[go-sprout documentation](https://docs.gosprout.github.io/) for those.

### Color / formatting

| Function | Signature | Behavior |
|---|---|---|
| `green`, `yellow`, `red`, `cyan`, `blue` | `(format string, a ...any) string` | `fatih/color` `*String` functions. Reject a non-string/nil arg for `format` — see [CONVENTIONS.md § Never trust a field to be present](CONVENTIONS.md#never-trust-a-field-to-be-present). |
| `bold` | `(format string, a ...any) string` | `color.New(color.Bold).SprintfFunc()`. |
| `colorAgo` | `(kubeDate string) string` | Colored relative age from an RFC3339 Kubernetes timestamp (`time.Since`). Accepts exactly one layout; anything else silently returns the zero time — see [CONVENTIONS.md § Dates](CONVENTIONS.md#dates). Only meaningful for past timestamps. |
| `colorAgoUnixNano` | `(unixNano interface{}) string` | Same as `colorAgo`, from a Unix-nanosecond timestamp instead. |
| `colorDuration` | `(duration time.Duration) string` | Colors a duration by rough magnitude. |
| `colorPercent` | `(format string, percent float64) string` | Colors a percentage red/yellow/regular by how close to 100 it is. |
| `startedAfterClause` | `(createdKubeDate, startedKubeDate string) string` | The `, started after <duration>` clause on `status_summary_line`. |
| `colorBool` | `(cond bool, str string) string` | Colors `str` green/red by `cond`. |
| `colorKeyword` | `(phase string) string` | Colors a well-known keyword (`Running`, `Failed`, `Pending`, ...) by its usual health meaning. |
| `markRed`, `markYellow`, `markGreen` | `(regex, s string) string` | Colors every regex match within `s` (rest of the string untouched) — used e.g. to color `+`/`-` lines of a unified diff. |
| `redIf`, `redBoldIf` | `(cond interface{}, str string) string` | `str` in red (bold for `redBoldIf`) when `cond` is truthy, unchanged otherwise. |

### Time

| Function | Signature | Behavior |
|---|---|---|
| `agoSuffix` | `() string` | The trailing `" ago"`/`""` clause paired with `colorAgo`, honoring `--absolute-time`. |
| `forOrSince` | `() string` | `"for"` or `"since"` depending on `--absolute-time`, used before a `colorAgo` call in condition rendering. |
| `relativeTime` | `(kubeDate string) string` | Relative-time rendering without color. |
| `untilClause` | `(t time.Time) string` | `" (in <duration>)"` for a **future** timestamp (expiry, scheduled time) — the future-safe counterpart to `colorAgo`, see [CONVENTIONS.md § Dates](CONVENTIONS.md#dates). |
| `withinLastHour` | `(kubeDate interface{}) bool` | Whether a timestamp is within the last hour of `cfg.Now()`. |
| `cronNextTime` | `(schedule string, timezone interface{}) string` | Next scheduled run of a cron expression, honoring an optional IANA timezone. |

### Quantities and numbers

| Function | Signature | Behavior |
|---|---|---|
| `quantityToFloat64`, `quantityToInt64` | `(str string) float64\|int64` | Parses a Kubernetes `resource.Quantity` string (`"100Mi"`, `"250m"`). |
| `percent` | `(x, y float64) float64` | `x/y*100`. |
| `humanizeSI` | `(unit string, input float64) string` | SI-scaled humanized value (`humanize.SIWithDigits`), e.g. `1.5GB`. |
| `humanizeSIPair` | `(unit string, a, b float64) string` | Two related values under one shared SI unit scaled to the larger, e.g. `32.8/33.6GB`. |
| `addFloat64`, `subFloat64`, `divFloat64` | `(...)` | `addFloat64(i ...interface{}) float64` sums (casting each); `subFloat64(a, b) = b - a`; `divFloat64(a, b) = b / a`. |

### List / map helpers

| Function | Signature | Behavior |
|---|---|---|
| `getMatchingItemInMapList` | `(searchFor map[string]interface{}, mapList []interface{}) map[string]interface{}` | First item in `mapList` whose fields (dotted-path keys in `searchFor` supported) match `searchFor`. The idiom behind every `getMatchingItemInMapList (dict "type" "Ready") .StatusConditions` lookup. |
| `sortMapListByKeysValue` | `(key string, mapList []interface{}) []interface{}` | Stable-sorts by `key`'s string value, ascending. |
| `sortMapListByFloatKeysValueDesc` | `(key string, mapList []interface{}) []interface{}` | Sorts by `key`'s numeric value, descending. |
| `fieldsV1Paths` | `(fieldsV1 map[string]interface{}) []string` | Human-readable field paths from a `metadata.managedFields[].fieldsV1` structure. |
| `sortByRevisionAnnotation`, `sortByRevisionField` | `(objs []interface{}) []interface{}` | Sorts by the `deployment.kubernetes.io/revision` annotation, or a numeric `.revision` field, ascending. |

### Kubernetes-domain helpers

| Function | Signature | Behavior |
|---|---|---|
| `signalName` | — | Named signal for a container exit-code/termination signal number. |
| `isStatusConditionHealthy` | `(condition map[string]interface{}) bool` | Whether a `status.conditions[]` entry counts as healthy, honoring the "abnormal-true" polarity exceptions documented in [README.md](README.md#scope-and-extending-it). |
| `evictionHeadroom` | `(threshold string, current, total float64, unit string) evictionSignal` | Correlates a kubelet eviction-hard threshold against current headroom for that resource. Returns a struct with `Threshold`, `Current`, `AtRisk`, `Tripped`, `OK` fields. Pass `current < 0` when the measurement isn't known. |
| `evictionAnnotation` | `(threshold string, current, total float64, unit string) string` | Rendered one-line form of the above. |
| `quotaRolloutHeadroom` | `(quotas []interface{}, workload map[string]interface{}) quotaHeadroomReport` | Backs the `quota_headroom` shared helper above. Returns `{ExtraPods int; Quotas []{Name string; Shortfalls []{Resource,Need,Free,Used,Hard string}}}`. |
| `labelSelector` | `(s map[string]interface{}) string` | Renders a Kubernetes `LabelSelector` (`matchLabels` **and** `matchExpressions`) as one string — required per [CONVENTIONS.md § Label selectors](CONVENTIONS.md#label-selectors) instead of hand-joining `matchLabels`. |
| `taintsNotToleratedByPod` | `(nodeTaints, tolerations []interface{}) []interface{}` | Node taints a Pod's tolerations don't cover. |
| `nodeCloudProvider` | `(providerID string, labels map[string]interface{}) string` | Cloud provider name inferred from `spec.providerID`/labels. |
| `formatNodeSelector`, `formatNodeSelectorTerms` | `(...)` | Human-readable rendering of a `nodeSelector` map / `nodeAffinity` `nodeSelectorTerms` list. |
| `podHardConstraintRequirements` | `(nodeSelector map[string]interface{}, terms []interface{}) []interface{}` | Normalizes a Pod's hard `nodeSelector` + required `nodeAffinity` into one requirement list, for cross-checking against NodePool requirements. |
| `karpenterUnsatisfiableKeys` | `(podRequirements []interface{}, nodePools []interface{}) []string` | Requirement keys no visible Karpenter NodePool could ever satisfy. |
| `karpenterDisqualifyingKey` | `(nodePoolRequirements, podRequirements []interface{}) string` | The specific key that disqualifies one NodePool from a Pod's requirements. |
| `networkPolicyPolicyTypes`, `calicoPolicyTypes` | `(spec map[string]interface{}) []string` | Effective `Ingress`/`Egress` policy types, applying each API's own default-when-absent rule. |
| `ciliumPolicyDirections` (func `ciliumPolicyDirectionsForTemplate`) | `(obj map[string]interface{}, podLabels map[string]interface{}) []string` | Ingress/egress directions a CiliumNetworkPolicy's rules actually restrict for the given Pod labels. |
| `qualifyKind` | `(kind, group string) string` | `"Kind.group"` (empty group renders as bare `Kind`) — the same qualification scheme `findTemplateName` uses to disambiguate a Kind that exists in more than one API group. |
| `hostnameIntersections` | `(listenerHostname string, routeHostnames interface{}) []string` | Gateway API hostname-matching between a Listener and a Route. |
| `istioHost` | `(host, namespace string) IstioHostRef` | Resolves an Istio host reference. Returns `{Key, Name, Namespace string; InCluster bool}`. |

### Secrets and certificates

All take a `RenderableObject` (the Secret/ConfigMap/CSR) and return a `map[string]interface{}`/`[]map[string]interface{}`
shaped for direct template consumption (already-decoded values, parsed cert fields), so a user template
never has to base64-decode `.data` itself.

| Function | Covers |
|---|---|
| `parseTLSSecretCertificate` | A `kubernetes.io/tls` Secret's certificate, cross-checked against expected hostnames. |
| `certificatesInSecret`, `certificatesInConfigMap` | Every PEM certificate found in a Secret's/ConfigMap's data, each entry shaped like `parseTLSSecretCertificate`'s (`NotBefore`/`NotAfter`/`Expired`/...) — the shape `certificate_validity_line` expects. |
| `certificateInCSR`, `certificateRequestInCSR` | The certificate embedded in a `CertificateSigningRequest`/`cert-manager` `CertificateRequest`. |
| `parseDockerConfigSecret` | A `kubernetes.io/dockerconfigjson`/`dockercfg` Secret's registries. |
| `parseBasicAuthSecret`, `parseSSHAuthSecret`, `parseServiceAccountTokenSecret` | The respective typed Secret's decoded fields. |
| `parseBootstrapTokenSecret` | A `bootstrap.kubernetes.io/token` Secret's fields. |
| `parseHelmReleaseSecret` | A Helm release-storage Secret (`helm.sh/release.v1`): decompresses/decodes the release, exposes chart/values/manifest. |
| `helmReleaseManifestResources` | `(manifest string) []map[string]interface{}` — every object embedded in a Helm release manifest string. |
| `secretDataKeys` | `(secret RenderableObject) []string` — key names in `.data`/`.stringData` without their (possibly sensitive) values. |

### Crossplane

| Function | Signature | Behavior |
|---|---|---|
| `crossplaneManagedResourceDrift` | `(forProvider, atProvider interface{}) crossplanedrift.Result` | Structural diff between a managed resource's desired (`spec.forProvider`) and observed (`status.atProvider`) state. See `pkg/plugin/crossplanedrift.Result` for the full field set (`Eligible`, `TotalConfigured`, `DriftEntries`, `UnifiedDiff`, `RedactedPaths`, ...). |
| `crossplaneDriftLabel` | `(syncedStatus string, managementPolicies interface{}) map[string]string` | The label/annotation to show alongside a drift result, given the resource's `Synced` condition and management policy. |

### Misc.

| Function | Signature | Behavior |
|---|---|---|
| `ip` | `(ip string) string` | Passes `ip` through unchanged, except under `--test-hack` where it's replaced with a fixed `1.1.1.1` for deterministic test fixtures. |
| `renderGroupedTable` | `(leadingHeader string, groupLabels []interface{}, groupSpans []interface{}, rows []interface{}) string` | Renders a column-aligned table with grouped column headers, accounting for ANSI color codes when computing visible width. |

## `RenderableObject` methods and fields (stable)

`RenderableObject` (`pkg/plugin/renderable.go`) wraps `unstructured.Unstructured` and is the `.`
context every Kind template renders against. It's also what a `dict`'s `"ctx"`/`"pod"`/`"obj"` entry
holds in most shared helpers above. Every exported method is reachable from a template
(`.MethodName arg1 arg2`); the `Config` field's own type is `*viper.Viper`, so `.Config.GetBool "flag"`
etc. is viper's public API, not this project's.

### Object accessors

`Kind()`, `APIVersion()`, `Name()`, `Namespace()`, `Spec()`, `Status()`, `Metadata()`, `Annotations()`,
`Labels()` — typed accessors into the underlying object map, each defaulting to an empty map/string
rather than nil/panicking when absent. `StatusConditions()` returns `status.conditions` sorted by
`type` ascending (controllers don't guarantee an order — see #787). `String()` returns `"Kind/name[ns]"`
for logging. `KStatus()` returns `*kstatus.Result` (`sigs.k8s.io/cli-utils/pkg/kstatus/status`) —
`.Status.String`, `.Message`, `.Conditions`.

### Rendering / inclusion

- **`Include(templateName string, data interface{}) (string, error)`** — executes another named
  template with `data` as its `.`, returns the rendered string (or an error, which a template rarely
  checks explicitly — an unhandled error from a top-level `Include` call propagates up and aborts the
  whole object's render, see [CONVENTIONS.md](CONVENTIONS.md#never-trust-a-field-to-be-present)).
- **`IncludeRenderableObject(obj RenderableObject) string`** — renders `obj` through its own Kind
  template (`findTemplateName` on `obj.Kind()`), the primitive behind every `--deep` inline render.
- **`LiveQueriesDisabled() bool`** — true under `--shallow` or `--local`. Every `KubeGet*` method below
  already checks this itself and returns empty/zero rather than querying, so a template only needs to
  call this directly when deciding whether to render a "would query but didn't" note, not to guard the
  `KubeGet*` call itself.

### Live cluster queries

All silently return empty/zero (never an error a template sees) when the query fails or
`LiveQueriesDisabled()` is true — see [CONVENTIONS.md § Rendering depth](CONVENTIONS.md#rendering-depth).

| Method | Returns |
|---|---|
| `KubeGet(namespace string, args ...string) []RenderableObject` | Objects matching a `kubectl get -n <namespace> <args...>`-shaped query. |
| `KubeGetFirst(namespace string, args ...string) RenderableObject` | First match, or a `RenderableObject` with a nil `Object` when nothing's found (check with `if $obj.Object`, not `if $obj`). |
| `KubeGetByLabelsMap(namespace, resourceType string, labels map[string]interface{}) []RenderableObject` | Objects of `resourceType` matching a label selector built from `labels`. |
| `KubeGetEvents() RenderableObject` | This object's Events, as a List-shaped `RenderableObject` (`.Object.items`). |
| `KubeGetOwners() OwnersResult` | `{Owners []RenderableObject; Orphans []metav1.OwnerReference}` — resolved `ownerReferences`, split into found vs. dangling. |
| `KubeGetIngressesMatchingService(namespace, svcName string) []RenderableObject` | Ingresses whose rules route to `svcName`. |
| `KubeGetRoutesMatchingService(namespace, svcName string) []RenderableObject` | Gateway API routes (HTTPRoute/GRPCRoute/TCPRoute/UDPRoute/TLSRoute) whose `backendRefs` reference `svcName`. |
| `KubeGetServicesMatchingLabels(namespace string, labels map[string]interface{}) []RenderableObject` | Services whose selector is a subset of `labels`. |
| `KubeGetPodDisruptionBudgetsMatchingLabels(namespace string, labels map[string]interface{}) []RenderableObject` | PDBs whose `spec.selector` matches `labels`. |
| `KubeGetServicesMatchingPod(namespace, podName string) []RenderableObject` | Services actually fronting `podName`, via EndpointSlice membership. |
| `KubeGetEndpointSlicesForService(namespace, serviceName string) []RenderableObject` | EndpointSlices for a Service. |
| `KubeGetNetworkPoliciesMatchingPod(namespace string, podLabels map[string]interface{}) []RenderableObject` | Upstream NetworkPolicies selecting `podLabels`. |
| `KubeGetGatekeeperConstraintsMatchingNamespace() []RenderableObject` | Called on a Namespace object. Live Gatekeeper Constraints (any installed kind) whose `spec.match` would admit objects in this Namespace. |
| `KubeGetCiliumNetworkPoliciesMatchingPod`, `KubeGetCiliumClusterwideNetworkPoliciesMatchingPod` | `(namespace string, podLabels map[string]interface{})` / `(podLabels map[string]interface{})` — Cilium policy equivalents. |
| `KubeGetCalicoNetworkPoliciesMatchingPod`, `KubeGetCalicoGlobalNetworkPoliciesMatchingPod` | `(namespace string, podLabels map[string]interface{})` each — Calico policy equivalents. |
| `KubeGetNodeStatsSummary(nodeName string) map[string]interface{}` | kubelet `/stats/summary` for a Node. |
| `KubeGetNodeConfigz(nodeName string) map[string]interface{}` | kubelet `/configz` for a Node. |
| `KubeGetNodeHealthz(nodeName string) string` | kubelet `/healthz` body, or `"unreachable: <err>"`. |
| `KubeGetPodMetrics(namespace, name string) RenderableObject` | `metrics.k8s.io` PodMetrics. |
| `KubeGetNodeMetrics(name string) RenderableObject` | `metrics.k8s.io` NodeMetrics. |
| `KubeMetricsUnavailableReason() string` | Why `metrics.k8s.io` isn't usable right now, or `""` if healthy/unchecked. |
| `KubeGetContainerLogs(namespace, podName, containerName string, previous bool, tailLines int) string` | Up to `tailLines` of container log output. |
| `KubeGetNonTerminatedPodsOnNode(nodeName string) []RenderableObject` | Non-terminal Pods scheduled to a Node. |
| `KubeGetUnifiedDiffString(resourceOrKind, namespace, nameA, nameB string) string` | Unified diff between two objects of the same kind, with noisy fields (resourceVersion, managed fields, revision annotations, ...) stripped. |

### Diagnostics

- **`RolloutStatus(obj RenderableObject) map[string]interface{}`** — `{done bool; message, error string}`
  via `kubectl`'s own `polymorphichelpers.StatusViewerFor` (works for the same kinds `kubectl rollout status`
  does: Deployment, DaemonSet, StatefulSet).
- **`StatefulSetRollbackTrap() map[string]interface{}`** — called on a StatefulSet. Returns
  `{pod RenderableObject; podRevision, targetRevision string}` when the object is caught in the
  [kubernetes/kubernetes#67250](https://github.com/kubernetes/kubernetes/issues/67250) forced-rollback
  trap, `nil` otherwise.

## Everything else is internal

Every other `{{define}}` name in `pkg/plugin/templates/*.tmpl` — 87 of them — is called only from
within the file that defines it (a Kind's own private sub-blocks) and is not part of this contract,
regardless of how generically it's named or how long it's been stable in practice. Grouped by file for
reference (not a call contract — names here can be renamed, split, or merged freely):

- **`common.tmpl`**: `crossplane_composition_ref`, `crossplane_composed_resources`,
  `crossplane_managed_resource_drift`, `crossplane_managed_resource_details`,
  `gatekeeper_constraint_fallback`, `flux_object_management`, `flux_revision`,
  `custom_application_details` (special — see [above](#the-user-override-point-custom_application_details)),
  `network_policy_selection_summary`, `cilium_policy_selection_summary`, `calico_policy_selection_summary`,
  `pod_node_problem_flags`, `pod_network_policy_flags`, `HorizontalPodAutoscaler.summary`'s sibling
  `matching_hpas`, `VerticalPodAutoscaler.summary`'s sibling `matching_vpas`,
  `PodDisruptionBudget.summary`'s sibling `pdb_conflict_warning`, `volumeattachment_diagnosis`,
  `rwop_holder_diagnosis`.
- **`policy_report_common.tmpl`**: `policy_report_findings`, `policy_report_finding_line`.
- **`Pod.tmpl`** (26): `pod_status_summary_line`, `pod_placement_constraints`,
  `pod_karpenter_compatibility`, `pod_topology_constraints`, `pod_node_problems`,
  `pod_memory_eviction_risk`, `pod_init_containers`, `pod_containers`, `pod_priorityclass_summary`,
  `pod_runtimeclass_overhead`, `pod_volume_inline_stats`, `pod_volume_line`,
  `pod_volume_configmap_secret_problem`, `pod_volumes`, `pod_storage_locality`,
  `pod_condition_arrow_label`, `pod_conditions_summary`, `container_usage`,
  `container_requests_limits`, `container_limitrange_defaults_note`, `container_status_summary`,
  `container_state_summary`, `probe_summary`, `lifecycle_hook_summary`, `pod_ephemeral_containers`,
  `exit_code_summary`.
- **`Node.tmpl`** (11): `node_addresses`, `taints`, `node_kubelet_summary`, `node_eviction_annotation`,
  `node_lease`, `node_pod_details`, `node_reserved_clause`, `node_capacity`, `node_stats_summary_fs`,
  `node_stats_resources`, `node_stats_fs`.
- **`VirtualService.tmpl`** (7): `istio_route_destinations`, `istio_destination`,
  `istio_destination_problems`, `istio_retry_summary`, `istio_fault_percentage`, `istio_match_summary`,
  `istio_string_match`.
- **`DestinationRule.tmpl`** (2): `istio_traffic_policy`, `istio_load_balancer`.
- **`Service.tmpl`** (4): `service_traffic_config`, `matching_ingresses`, `matching_routes`,
  `endpoint_slice_endpoint`.
- **`StatefulSet.tmpl`** (2): `statefulset_volume_claims`, `recent_statefulset_rollouts`.
- **`DaemonSet.tmpl`** (2): `daemonset_replicas_status`, `recent_daemonset_rollouts`.
- **`Deployment.tmpl`** (1): `recent_deployment_rollouts`.
- **`Job.tmpl`** (2): `job_failed_pod_summary`, `job_indexed_details`.
- **`Namespace.tmpl`** (1): `namespace_psa_level`.
- **`LimitRange.tmpl`** (1): `limit_range_item`.
- **`MutatingWebhookConfiguration.tmpl`** (1): `mwc_webhook_entry`.
- **`ValidatingWebhookConfiguration.tmpl`** (1): `vwc_webhook_entry`.
- **`Secret.tmpl`** (1): `helm_release_manifest_entry`.

Every Go identifier not listed in the sections above — every unexported function/method in
`pkg/plugin/*.go`, and every exported one not registered in `funcMap()`/not a `RenderableObject`
method (e.g. the `crossplanedrift`/`calicoselector` package internals, `evictionSignal`/
`quotaHeadroomReport` field shapes beyond what's documented above) — is likewise internal.

## Borderline cases worth a second look before #809

A handful of names above look prunable by a naive "single caller" heuristic but aren't, and a few
genuinely-internal ones are one hop away from a stable helper. Flagged here so the eventual
enforcement/tree-separation work (tracked separately from this document) doesn't trip over them:

1. **`custom_application_details`** — one textual caller (`application_details`), but its doc comment
   makes it an explicit user override point (see [above](#the-user-override-point-custom_application_details)).
   Must stay reachable and stay named exactly this, independent of caller count.
2. **`gatekeeper_constraint_fallback`** — one caller (`DefaultResource`, a different file), but it's
   the sole routing point for every dynamically-generated Gatekeeper Constraint Kind that has no
   dedicated `<Kind>.tmpl`. Treat as load-bearing infrastructure even though it's marked internal.
3. **`matching_hpas`, `matching_vpas`, `pdb_conflict_warning`, `network_policy_selection_summary`/
   `cilium_policy_selection_summary`/`calico_policy_selection_summary`,
   `pod_node_problem_flags`/`pod_network_policy_flags`** — each is one hop from a stable Category B
   helper (`HorizontalPodAutoscaler.summary`, `VerticalPodAutoscaler.summary`,
   `PodDisruptionBudget.summary`, `Pod.summary`, called only by it). Free to rename in isolation,
   but check the caller when doing so.
4. **`resource_health_summary`/`generic_health_summary`** — technically one-hop-from-B by caller
   count, but their own doc comments describe them as meant for general reuse in mixed-kind lists;
   promoted to Category B above rather than left internal.
5. **`volumeattachment_diagnosis`/`rwop_holder_diagnosis`** — defined in `common.tmpl` but each has
   exactly one external caller (`PersistentVolume.tmpl`/`PersistentVolumeClaim.tmpl` respectively).
   Could move into that sole caller's file with zero impact on anything in this document.
6. **`load_balancer_ingress`** (defined in `Ingress.tmpl`, called by `Service.tmpl` too) and **`event`**
   (defined in `Event.tmpl`, called by `common.tmpl`'s `events`) are the only two Category B names
   *not* defined in one of the three `*common.tmpl` files — a search scoped to just those three files
   would miss them.
7. **`vwc_webhook_entry`** (`ValidatingWebhookConfiguration.tmpl`) and **`mwc_webhook_entry`**
   (`MutatingWebhookConfiguration.tmpl`) implement near-identical webhook-entry rendering logic
   independently. Both stay internal today; a future refactor promoting one shared version to
   `common.tmpl` would need to add it to the Category B list above at that point.
8. No dead code was found: every one of the 193 `{{define}}` names has at least one verified caller
   (either a `{{template}}`/`Include` invocation, or — for the 63 Kind names — reachability via
   `findTemplateName`).

## Versioning policy

kubectl-status ships versioned releases (goreleaser, git-tag-driven, `main.version={{ .Summary }}`),
but until now that versioning said nothing about the template API specifically — a rename or signature
change to a name in this document could ship in any release with no signal beyond reading the diff.

Going forward:

- **A breaking change to any name/signature documented above** (removing or renaming a `{{define}}`
  block or FuncMap function listed here; changing a documented `dict` key's meaning, a function's
  parameter order/types, or a `RenderableObject` method's signature or return shape) must:
  1. Update this document in the same PR.
  2. Add an entry under **`### Breaking template API changes`** in the `[Unreleased]` section of
     [`CHANGELOG.md`](CHANGELOG.md), describing what changed and how a custom
     `~/.kubectl-status/templates/*.tmpl` should adapt.
  3. Be called out in the PR description as a template-API break, so a reviewer checks (1) and (2)
     were actually done.
- **Anything not in this document** (the "Everything else is internal" list, any unexported Go
  identifier, anything in `pkg/plugin/crossplanedrift`/`pkg/plugin/calicoselector` not surfaced through
  a documented FuncMap entry) can change in a patch release with no changelog entry — that's the
  entire point of the stable/internal split above.
- A change that only *adds* a new stable name (a new shared helper, a new FuncMap function) is not
  breaking and doesn't need a changelog entry, though it's welcome to have one.

This is a documentation/process convention, not a build-time check — nothing currently fails CI if a
PR removes a stable name without following it. Enforcing the boundary mechanically (so an internal
rename genuinely can't reach a user template, and so a stable-list break could in principle be caught
automatically) is out of scope for this document and tracked in the related template-reorganization
issues.
