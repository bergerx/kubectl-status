# resource-status kubectl

A `kubectl` plugin to print a human-friendly output that focuses on
the status fields of the resources in kubernetes.

In most cases replacing a `kubectl get ...` with an `kubectl resource-status`
would be sufficient.

## Quick Start

```
kubectl krew install resource-status
kubectl resource-status
```

See [usage](doc/USAGE.md) for some sample usage examples.

## Some Example Outputs

Sample pod output from a minikube instance:
![Minikube components](doc/minikube-components.png)
[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fbergerx%2Fkubectl-resource-status.svg?type=shield)](https://app.fossa.io/projects/git%2Bgithub.com%2Fbergerx%2Fkubectl-resource-status?ref=badge_shield)

Some pods got KILL and TERM signals, has containers with multiple restarts:
![Restarts with Signals](doc/init-signal-restart.png)

Some falinig InitContainer and Container
![Failing InitContainer](doc/failing-init-container.png)
![Failing Container](doc/failing-container.png)



## License
[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fbergerx%2Fkubectl-resource-status.svg?type=large)](https://app.fossa.io/projects/git%2Bgithub.com%2Fbergerx%2Fkubectl-resource-status?ref=badge_large)