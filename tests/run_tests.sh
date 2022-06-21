#!/bin/bash

set -x
kubectl config use-context kind-kind

go watch_and_save(ns, resource=Deployment, name=test)

create_deployment(ns, name, image)


# TODO: have a parameter to save the resulting manifests
#       * prepare steps that would allow us to get the manifests for all the case down below
# TODO: how to ensure all templates are covered

# cases to cover

# Common
# - DONE suspended
# - events / no events
# - events warning, with/out lastTimestamp, with/out count, with reportingComponent, reportingInstance, source.*
# - recent updates
# - replicas status
# - without namespace
# - has no/owners
# - has no/start time
# - has no/completion time
# - .Status.phase, .Status.state, .Status.reason
# - being deleted
# - DONE has no/generation mistmatch
# - conditions summary (without any fields) TODO: make extendable
# - with helm chart details, with partial helm chart details, old chart labels , from different namespace
# - from addonManager
# - is a clsuterService

# POD
# - standalone pod
# - QOS levels
# - with/out message
# - with/out init container
# - failed init container
# - has metrics / no metrics
# - has PVCs
# - has non/memory emptyDirs (new kind cluster with certain disk space)
# - has no/matching services
# - not yet scheduled
# - not initialized (has inits but not yet ran)
# - not ready
# - not healthy

# Deployment
# - has no .Status.(replicas,readyReplicas,availableReplicas,updatedReplicas)
# - not RolloutStatus.done, with/out (message,error)
# - has no ready replicas
# - (ready != replicas) && rollout not done
# - has unavailable replicas && rollout not done
# - has no previous rollouts
# - has 2/3 replicasets
