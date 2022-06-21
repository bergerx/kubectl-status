#!/bin/bash
# use https://github.com/marketplace/actions/setup-k3d-k3s

# Store the current namespace list
active_namespaces=()

add_namespace() {
  namespace="$1"
  active_namespaces+=${namespace}
  kubectl create ns $namespace
  kubectl label ns $namespace created-by-kubectl-status=1
}

delete_namespace() {
  namespace="$1"
  kubectl delete ns $namespace
  active_namespaces=( "${active_namespaces[@]/${namespace}}" )
}

function delete_active_namespaces {
  [ ${#active_namespaces[@]} -ne 0 ] && echo "Will delete current namespaces: ${active_namespaces[@]}"
  for namespace in "${active_namespaces[@]}"; do
    delete_namespace $namespace
  done
  kubectl delete ns -l created-by-kubectl-status
}

function exit_func {
  echo Attempting to delete all leftover deployed artifacts!
  pkill -P $$
  delete_active_namespaces
  rm -rf *.result
  wait
}
trap exit_func EXIT

execute_watch_manifest() {
  manifest_name=$1

  wait_for_list=$(grep '^# wait-for:' $manifest_name | cut -d" " -f3-)
  test_name=${manifest_name//.*}
  namespace=$test_name
  kind=$(grep ^kind: ${manifest_name} | cut -d' ' -f2)
  > ${manifest_name}.result
  
  
  add_namespace $namespace
    kubectl get -o yaml -n $namespace -w $kind --output-watch-events > generated/${manifest_name}.raw_yamls &
    kubectl st -n $namespace -w $kind | gsed --unbuffered 's/\b[0-9]\+s\b/Xs/g' >> ${manifest_name}.result &
      sleep 1

      kubectl apply -n $namespace -f $manifest_name
      for wait_for in $wait_for_list; do
        kubectl wait -n $namespace --for=$wait_for -f $manifest_name --timeout 30s --allow-missing-template-keys
      done

      kubectl st -n $namespace -f $manifest_name | gsed --unbuffered 's/\b[0-9]\+s\b/Xs/g' >> ${manifest_name}.result

      kubectl delete -n $namespace -f $manifest_name
      kubectl wait -n $namespace --for=delete --all all

      sleep 1
    kill %2
    kill %1
  delete_namespace $namespace
}

test_watch_manifest() {
  manifest_name=$1

  execute_watch_manifest "$manifest_name"
  diff -u generated/${manifest_name}.expected ${manifest_name}.result
  ret=$?
  rm ${manifest_name}.result
  return $ret
}

update_expected_watch_manifest_results() {
  manifest_name=$1

  execute_watch_manifest "$manifest_name"
  mv ${manifest_name}.result generated/${manifest_name}.expected
}

#command=${1:?'You should pass one of "test" or "update"'}
command=${1:-update}

for manifest in *.yaml; do
  echo Running test for $manifest.
  case $command in
  test)
    set -m
    if ! output=$(test_watch_manifest $manifest); then
      echo "Failed test for manifest $manifest:"
      echo "==================================="
      echo "$output"
      exit 1
    fi
    ;;
  update)
    set -m
    update_expected_watch_manifest_results $manifest &
    ;;
  *)
    echo 'You should pass one of "test" or "update"'
    exit 1
    ;;
  esac
done
wait
