
## Usage
The following assumes you have the plugin installed via

```shell
kubectl krew install status
```

### Show status of some resources

In most cases replacing a `kubectl get ...` with an `kubectl status`
would do it.

```shell
kubectl status pods --all-namespaces                             # Show status of all pods in all namespaces
kubectl status pods                                              # Show status of all pods in the current namespace
kubectl status nodes                                             # Show status of all nodes
kubectl status pod my-pod                                        # Show status of a particular pod
kubectl status deployment my-dep                                 # Show status of a particular deployment
kubectl status node --selector='node-role.kubernetes.io/master'  # Show status of nodes marked as master
```

## How it works

Just a different representation of the kubernetes resources.

Internally uses go templates to print a human-friendly output that focuses on
the status fields of the resources in kubernetes.
