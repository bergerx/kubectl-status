# Changelog

Format loosely follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). This project doesn't
otherwise track a change log for every release (GitHub's auto-generated release notes cover the general
case) — this file exists specifically to carry the **"Breaking template API changes"** section described
in [TEMPLATE-API.md § Versioning policy](TEMPLATE-API.md#versioning-policy).

A PR that breaks a name documented in [TEMPLATE-API.md](TEMPLATE-API.md) (removes/renames a stable
`{{define}}` block or FuncMap function, changes a documented `dict` argument's meaning, or changes a
`RenderableObject` method's signature or return shape) must add an entry here, under `[Unreleased]`,
describing what changed and how a `~/.kubectl-status/templates/*.tmpl` override should adapt. A change
that only adds a new stable name doesn't need an entry (though it's welcome to have one). Everything
outside the documented stable surface — internal template helpers, unexported Go functions — can change
without an entry here.

## [Unreleased]

### Breaking template API changes

- `callerNamespace` on `Deployment.summary`, `StatefulSet.summary`, `DaemonSet.summary`,
  `ReplicaSet.summary`, `Job.summary`, `Ingress.summary`, `Service.summary`,
  `PodDisruptionBudget.summary`, `HorizontalPodAutoscaler.summary`,
  `VerticalPodAutoscaler.summary`, and
  `HTTPRoute.summary`/`GRPCRoute.summary`/`TCPRoute.summary`/`UDPRoute.summary`/`TLSRoute.summary`
  was previously accepted but silently ignored (documented as "unused, kept for the uniform
  contract"); it is now honored the same way every other `"<Kind>.summary"` template already
  honors it — passed through to `resource_ref` so the object's own namespace is dropped from the
  one-line summary only when it equals `callerNamespace`. A `~/.kubectl-status/templates/*.tmpl`
  override of any of these `.summary` names that relied on the namespace always being omitted
  should pass its own namespace as `callerNamespace` explicitly if it wants that behavior back.
