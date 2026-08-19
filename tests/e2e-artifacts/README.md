# Sample manifests for live e2e test prep

This file holds setup commands and sample manifests for third-party tools whose CRDs kubectl-status
renders, kept here as reference material for whoever picks up live e2e coverage for them. See
CONTRIBUTING.md's "Running e2e Tests Locally" section for how the two-tier test suite works and the
recipe for adding a new live e2e subtest (and for choosing which entry point it runs under).

None of the gaps below currently have an open GitHub issue — they're test-coverage gaps for
templates that already render correctly under Tier 1 (static fixtures), just not yet exercised by
a live e2e subtest. Pick one up by following the recipe in CONTRIBUTING.md; no need to file an
issue first, a PR adding the subtest is enough.

---

## kube-prometheus-stack

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm install kube-prometheus-stack prometheus-community/kube-prometheus-stack \
  -n monitoring --create-namespace \
  --set grafana.service.type=NodePort
```

Access Grafana:
```bash
kubectl port-forward -n monitoring svc/kube-prometheus-stack-grafana 3000:80
# default creds: admin / prom-operator
```

**Target e2e test:** `PrometheusRule.tmpl` has a real live-only branch — under `--deep` it calls
`KubeGetFirst` to inline the `Prometheus`/`PrometheusAgent`/`ThanosRuler`/`Alertmanager` a rule is
bound to via `.Status.bindings`. That field is only populated once the prometheus-operator actually
reconciles the rule against a running `Prometheus` object, which this section's `helm install`
already gives you. `t.Run("prometheusrule-bound", ...)` should `waitFor` `.status.bindings` to be
non-empty, then assert the default render lists the binding and the `--deep` render inlines the
bound object. `ServiceMonitor`/`PodMonitor` have no such branch — Tier 1 is enough for those.

---

## Flux

Installed by `ensureFlux` (cmd/e2e_helpers_test.go), which applies the release's `install.yaml` --
version pinned as `FLUX_VERSION` in hack/versions.env. `flux install` does the same thing if you
have the CLI and want to poke at a cluster by hand.

### Sample Helm workload (nginx via HelmRelease)

```yaml
# helmrepository-nginx.yaml
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: bitnami
  namespace: flux-system
spec:
  interval: 30m
  url: https://charts.bitnami.com/bitnami
---
# helmrelease-nginx.yaml
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: nginx
  namespace: default
spec:
  interval: 10m
  chart:
    spec:
      chart: nginx
      version: ">=18.x"
      sourceRef:
        kind: HelmRepository
        name: bitnami
        namespace: flux-system
```

### Sample Kustomize workload (podinfo)

```yaml
# clusters/minikube/podinfo/kustomization.yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: podinfo
  namespace: flux-system
spec:
  interval: 5m
  path: "./kustomize"
  prune: true
  sourceRef:
    kind: GitRepository
    name: flux-system
  targetNamespace: default
```

```yaml
# kustomize/kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/stefanprodan/podinfo//kustomize
```

```bash
flux get kustomizations
flux get helmreleases
```

Both templates now exist (issue #660). `Kustomization.tmpl`'s live-query branches — resolving
`status.inventory` entries into health summaries, inlining them under `--deep`, and flagging entries
whose object is gone — are covered by the Flux subtest of `TestE2EParallel`
(`runFluxSubtests`, cmd/e2e_flux_test.go). That subtest installs Flux and lets it reconcile
`tests/e2e-artifacts/flux-kustomization-inventory.yaml`: a `GitRepository` on podinfo pinned to an
immutable tag, and a `Kustomization` over it. Nothing in its `.status` is
written by the test — the inventory, `lastAppliedRevision` and the `kustomize.toolkit.fluxcd.io`
ownership labels on the applied objects are all kustomize-controller's, which is the only way an
assertion about them tests Flux rather than our reading of it.

Two details there are worth knowing before you write another Flux subtest:

- `spec.interval` is 60m, and the Kustomization does not watch what it applies. That's what lets the
  test delete a managed Service out-of-band and have it stay deleted — which is exactly the
  real-world state the `missing` marker reports, an inventory recording an apply that succeeded while
  the object is gone and the Kustomization goes on reporting Ready.
- `spec.targetNamespace` has to name the namespace outright. kustomize-controller does *not* default
  it to the Kustomization's own namespace; a source whose manifests set no namespace fails the apply
  with `namespace not specified`. It must stay in step with the test's `ns`.

**Remaining gap:** `HelmRelease.tmpl`'s live-query branches all run through `deep_render_ref` and so
need the real chart-source objects (`HelmRepository`/`OCIRepository`, the mirrored `HelmChart`) to
exist. Flux is already installed by `ensureFlux`, so what's left is a reachable chart repository and
a release that actually installs — the sample above is the starting point. It's Tier 1 only for now
— see `tests/artifacts/helmrelease-*`.

---

## ArgoCD

```bash
kubectl create namespace argocd
kubectl apply -n argocd -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml

# Access the UI
kubectl port-forward svc/argocd-server -n argocd 8080:443 &

# Get initial admin password
kubectl get secret argocd-initial-admin-secret -n argocd \
  -o jsonpath="{.data.password}" | base64 -d
```

### Sample Application (guestbook)

```yaml
# argocd-app-guestbook.yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: guestbook
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/argoproj/argocd-example-apps.git
    targetRevision: HEAD
    path: guestbook
  destination:
    server: https://kubernetes.default.svc
    namespace: guestbook
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
```

```bash
kubectl apply -f argocd-app-guestbook.yaml
kubectl get app -n argocd
```

See GitHub issue #661 for the tracked work item (add `Application.tmpl`).

---

## External Secrets Operator

```bash
helm repo add external-secrets https://charts.external-secrets.io
helm install external-secrets external-secrets/external-secrets \
  -n external-secrets --create-namespace
```

### Sample SecretStore + ExternalSecret (using a local Fake provider)

The `Fake` provider ships with ESO and requires no external backend — perfect for local testing:

```yaml
# eso-sample.yaml
---
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata:
  name: fake-store
  namespace: default
spec:
  provider:
    fake:
      data:
        - key: "/my-app/db-password"
          value: "supersecret123"
          version: "v1"
---
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: my-app-secret
  namespace: default
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: fake-store
    kind: SecretStore
  target:
    name: my-app-secret
    creationPolicy: Owner
  data:
    - secretKey: db-password
      remoteRef:
        key: /my-app/db-password
        version: v1
```

```bash
kubectl apply -f eso-sample.yaml
kubectl get externalsecret -n default   # STATUS=SecretSynced
kubectl get secret my-app-secret -n default
```

**Target e2e test:** `ExternalSecret.tmpl` has two `--deep` `KubeGetFirst` branches — one inlines
the referenced `SecretStore` (`.Spec.secretStoreRef`), the other inlines the synced target `Secret`.
`SecretStore.tmpl` itself has no live query (provider details are all spec/status, no cross-resource
lookups), so it only needs Tier 1. Add an `install-e2e-deps` entry for the ESO Helm chart, then
`t.Run("externalsecret-synced", ...)` — `waitFor(t, "externalsecret/my-app-secret", "condition=Ready")`,
assert the default render shows the `Store`/`Syncs to` refs, and the `--deep` render inlines both the
`SecretStore` and target `Secret`.

---

## Quick validation cheatsheet

| Tool | Verify |
|---|---|
| kube-prometheus-stack | `kubectl get pods -n monitoring` |
| Flux | `flux get all` |
| ArgoCD | `kubectl get app -n argocd` |
| ESO | `kubectl get externalsecret -A` |

---

## Job / CronJob — live-only gaps

Tier 1 static coverage for Job/CronJob rendering is fully done. What's left is the live-only
branches (all three are `KubeGetByLabelsMap`/`KubeGet` calls, unconditionally no-op'd under
`--shallow` regardless of template-level guards):

| Branch | Template | Trigger | Manifest needed |
|---|---|---|---|
| `Failed pods` | `Job.tmpl` | `.Status.failed` truthy | `job-failed` shape (backoffLimit exhausted) |
| `Pending pods` | `Job.tmpl` | `.Status.active` truthy + a Pod stuck `Pending` | not yet modeled — needs a Job whose pod can't schedule (e.g. an unsatisfiable resource request or node selector) |
| `Jobs` (owned-Job list) | `CronJob.tmpl` | any owned Job exists (via ownerReferences), active or historical | `cronjob-simple-active` or `-scheduled` shape |

**Target e2e tests:**
- `t.Run("job-failed", ...)` — `applyManifest`/`waitFor(t, "job/job-failed", "condition=failed")`, assert `Failed pods` section + `--deep` variant inlining the failed Pod.
- `t.Run("cronjob-with-jobs", ...)` — apply a `schedule: "*/1 * * * *"` CronJob, `waitFor` `.status.lastScheduleTime`, assert the `Jobs` section lists the owned Job + `--deep` variant.
- Pending-pods case needs a new manifest designed specifically for it (e.g. a Job requesting `cpu: 1000` on the single-node e2e cluster) before it can become a subtest.

---

## FlowSchema / PriorityLevelConfiguration — live-only gap

Tier 1 static coverage is fully done for both kinds. `FlowSchema.tmpl` has no live query at all, so
it's fully covered, period. The one gap is in `PriorityLevelConfiguration.tmpl`: its `FlowSchemas`
section calls `KubeGet "" "flowschemas"` to cross-reference which FlowSchemas route to a PLC —
unconditionally suppressed under `--shallow`, so no static fixture can exercise it. These are
built-in cluster resources (1.29+ uses `flowcontrol.apiserver.k8s.io/v1`) — no manifests to apply.

**Target e2e test:** `t.Run("prioritylevelconfiguration-flowschemas", ...)` in `TestE2EParallel`'s
pool (it only reads built-in cluster resources, so it perturbs nothing) — no
`applyManifest`/`waitFor` needed. `cmdTest{args: []string{"prioritylevelconfiguration/workload-high", "--v", "5"}}`
(non-`--shallow`) asserting the `FlowSchemas` section lists the expected cross-referenced
FlowSchemas, plus a `--shallow` variant asserting the section is absent.
