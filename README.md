# kubectl status

A `kubectl` plugin to print a human-friendly output that focuses on the status fields of the resources in kubernetes.

Just a different representation of the kubernetes resources (next to `get` and `describe`).

This plugin uses templates for well known api-conventions and has support for hardcoded resources,
not all resources are fully supported.

- [Installation](#installation)
  * [Upgrade](#upgrade)
- [Demo](#demo)
- [Features](#features)
- [Usage](#usage)
- [Development](#development)
  * [Release new version](#release-new-version)
  * [Guidelines for content](#guidelines-for-content)
  * [Guidelines for colorization](#guidelines-for-colorization)
- [License](#license)

## Installation

You can install `kubectl status` using the [Krew](https://github.com/kubernetes-sigs/krew), the package manager for kubectl plugins.

After you [install Krew](https://krew.sigs.k8s.io/docs/user-guide/setup/install/), just run:

```bash
kubectl krew install status
kubectl status --help
```

### Upgrade

Assuming you installed using [Krew](https://github.com/kubernetes-sigs/krew):

```bash
kubectl krew upgrade status
```

## Demo

Example Pod:
![pod](assets/pod.png)

Example StatefulSet:
![statefulset](assets/statefulset.png)

Example Deployment and ReplicaSet
![deployment-replicaset](assets/deployment-replicaset.png)

Example Service:
![service](assets/service.png)

## Features

* aims for ease of understanding the status of given resource,
* aligned with other kubectl cli subcomand usages (just like `kubectl get` or `kubectl describe`),
* uses colors for a better look and feel experince,
* errornous/impacting states are explicit and obvious,
* explicit messages for not-so-easy-to-understand status (e.g. ongoing rollout),
* goes the extra mile for better expressing the status (e.g. show spec diff for ongoing rollouts),
* compact, non extensive output to keep it sharp,
* no external dependencies, doesn't shell out, and so doesn't depend on client/workstation configuration

## Usage

In most cases replacing a `kubectl get ...` with a `kubectl status ...` would be sufficient.

Examples:
```bash
kubectl status pods                     # Show status of all pods in the current namespace
kubectl status pods --all-namespaces    # Show status of all pods in all namespaces
kubectl status deploy,sts               # Show status of all Deployments and StatefulSets in the current namespace
kubectl status nodes                    # Show status of all nodes
kubectl status pod my-pod1 my-pod2      # Show status of some pods
kubectl status pod/my-pod1 pod/my-pod2  # Same with previous
kubectl status svc/my-svc1 pod/my-pod2  # Show status of various resources
kubectl status deployment my-dep        # Show status of a particular deployment
kubectl status deployments.v1.apps      # Show deployments in the "v1" version of the "apps" API group.
kubectl status node -l node-role.kubernetes.io/master  # Show status of nodes marked as master
```

## Development

This project compiles the [templates](pkg/plugin/templates/templates.tmpl) file into the generated binary using
[rakyll/statik](https://github.com/rakyll/statik) which is triggered by `go generate`.
So you need to install `statik` first.

Then use make to get the compiled binary:

```bash
go get github.com/rakyll/statik
make
# the binary will be in bin/ folder
bin/status pods
```

Cross compile (used by github actions for new releases)

```bash
goreleaser release --skip-publish --skip-validate
# the binaries will be in dist/ folder
```

When working on a specific object its usually easier to save the object and work on it locally:

```bash
kubectl get pod test-pod -o yaml > test-pod.yaml
# make changes on the output
make
bin/status -t -f test-pod.yaml
```

kubectl-status follows below guidelines to have a consistent user experince across different resources.

### Release new version

This will release the head, gitlab's goreleaser action will publish the new release to krew index as well.

```bash
git tag vX.X.X
git push --tags
```

### Guidelines for content

* Aim output to be for humans "only", dont put any effort to make the output parse friendly.

* Try to keep the output as compact as possible, this is one of main differentiation points from `kubectl describe`.

* It's tempting to assume colors to be always working but don't forget that users will wan't to share the output
  with others by copy-paste, which results in losing ascii color codes.
  E.g. coloring "Ready" in green or red to indicate the status is usually not ideal,
       prefer "Not Ready" for faulty state in such cases.

* Not all status fields/values has to exist in the output, don't try to keep the value if you can make it more
  human-friendly.
  E.g. Prefer "Not Ready" over "Ready: false".

* Drop fields from the output if they are set to a well known defaults, or not so meaningful for understanding
  the status of the resource.
  E.g. podIP, hostIP, containerID, imageID of a pod doesn't hold much value for understanding the status of a pod.

* Be opinionated about how to represent the status, as long as it helps users to get the current status of
  the resource in question.

* Assume some level of knowledge and don't try to over explain, but explain non-obvious/edge cases and be explicit about
  possibly impacting states of resources.
  E.g. ongoing rollout, or no "Raady" Pods on a Deployment (usually means Outage),
       or a Service with no endpoints (again Outage).

* Dont include spec fields unless they have significant value for setting the context for the current status.
  E.g. knowing the .spec.replicas value is relevant for understanding the status of a ReplicaSet,
       but host values for ingress is pure spec.

* When it's not available in the status fields of the immediate resource, go the extra mile of doing further queries
  to obtain more information which may be helpful for users to better understand teh current status.
  E.g. fetch NodeMetrics and Pod on the node.

* Being aligned with the terminology used in the status fields is good but not mandatory.

* If there are conventions followed in the status fields, make them generic template and include it the DefaultResource
  template, so any other CRDs following the convention can also get benefit.
  E.g. observedGeneration, conditions, replicas.

### Guidelines for colorization

* Follow traffic lights convention, users expect them to be mapped to error/warning/ok consecutively.
  Prefer \[`red`/`yellow`/regular] over \[`red`/`yellow`/`green`] to suppress over-usage of `green`.

* Don't use `green` extensively, use when a dedicated status field indicates an explicit healthy status.
  E.g. use when Ready is True (or "active", "running"), but don't use when ready replicas mathing the desired replicas.

* Use `yellow` for issues which are well known to be transient.
  E.g. ongoing rollout.

* Use `bold red` for potential issues, that need attention.

* Prefer `red` over `bold red` for long explanation/description of a faulty state.
  E.g. for .status.conditions[].message field for a faulty condition.

* Prefer `bold red` over `red` if the highlighted text is a single word, camelCase, or PascalCase.
  E.g. for .status.conditions[].reason field for a faulty condition.

* When need to colorize a short key/value pair in a faulty state, prefer highlighting both the key and the value,
  not either.
  E.g. For `readyPodCount:0` paint the whole expression to `red` rathern than just key or value.

## License

Apache 2.0. See [LICENSE](./LICENSE).
