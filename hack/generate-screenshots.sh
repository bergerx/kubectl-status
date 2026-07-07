#!/usr/bin/env bash
# Regenerates the demo screenshots in assets/*.png from a live cluster.
#
# Requires:
#   - kubectl pointed at a disposable/dev cluster (e.g. minikube) -- this script
#     creates and deletes its own namespace, but does not touch any other
#     namespace or cluster-scoped state. Set ASSUME_KUBECONFIG_IS_DISPOSABLE=true
#     to skip the confirmation prompt (e.g. in CI).
#   - freeze (https://github.com/charmbracelet/freeze): go install github.com/charmbracelet/freeze@latest
#
# freeze renders with the "JetBrains Mono" font and exits 0 even when that font
# isn't installed, silently producing tofu-box images instead of text. This
# script registers it into a user fontconfig dir (no root needed) if missing.
set -euo pipefail

font_family="JetBrains Mono"

if ! command -v freeze >/dev/null 2>&1; then
  echo "freeze not found on PATH. Install it with: go install github.com/charmbracelet/freeze@latest" >&2
  exit 1
fi

if ! fc-list 2>/dev/null | grep -qi "jetbrains mono"; then
  echo "'${font_family}' not found by fontconfig; downloading and registering it for the current user..."
  font_dir="${HOME}/.local/share/fonts"
  mkdir -p "${font_dir}"
  curl -sSL -o "${font_dir}/JetBrainsMono-Regular.ttf" \
    "https://github.com/JetBrains/JetBrainsMono/raw/master/fonts/ttf/JetBrainsMono-Regular.ttf"
  fc-cache -f "${font_dir}"
  if ! fc-list 2>/dev/null | grep -qi "jetbrains mono"; then
    echo "Failed to register '${font_family}' with fontconfig; freeze would silently render blank/tofu images. Aborting." >&2
    exit 1
  fi
fi

context="$(kubectl config current-context)"
if [ "${ASSUME_KUBECONFIG_IS_DISPOSABLE:-}" != "true" ]; then
  cat <<EOF
This will, on kubectl context '${context}':
  - create and delete a namespace
  - enable the minikube 'metrics-server' addon, then disable it again when done
    (cluster-wide, affects other workloads using it in the meantime)
  - inject a made-up "RebootScheduled" status condition onto whatever node the
    demo pod lands on, to demonstrate the node-problem flag (no taint or
    cordon -- doesn't affect scheduling), and remove it again in cleanup
    (kubelet doesn't manage or clear condition types it doesn't know about, so
    this script removes it explicitly rather than relying on kubelet to)
EOF
  read -r -p "Continue? [y/N] " reply
  case "${reply}" in
    [yY]*) ;;
    *) echo "Aborted."; exit 1 ;;
  esac
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ns="kubectl-status-screenshots"
bin="$(mktemp -d)/kubectl-status"
assets="${repo_root}/assets"

echo "Building kubectl-status..."
go build -o "${bin}" "${repo_root}/cmd"

node_name=""

cleanup() {
  if [ -n "${node_name}" ]; then
    echo "Removing the injected RebootScheduled condition from Node/${node_name}..."
    # $patch: delete is the strategic-merge-patch directive for removing a
    # list item by its merge key ("type"), rather than by index.
    kubectl patch node "${node_name}" --subresource=status --type=strategic -p \
      '{"status": {"conditions": [{"$patch": "delete", "type": "RebootScheduled"}]}}' \
      >/dev/null 2>&1 || true
  fi
  echo "Disabling metrics-server addon..."
  minikube addons disable metrics-server >/dev/null 2>&1 || true
  echo "Deleting namespace ${ns}..."
  kubectl delete namespace "${ns}" --ignore-not-found --wait=false >/dev/null
}
trap cleanup EXIT

echo "Enabling minikube metrics-server addon..."
minikube addons enable metrics-server >/dev/null

echo "Creating namespace ${ns}..."
kubectl create namespace "${ns}"

echo "Applying demo manifests..."
kubectl apply -n "${ns}" -f "${repo_root}/tests/e2e-artifacts/deployment-demo.yaml"
kubectl apply -n "${ns}" -f "${repo_root}/tests/e2e-artifacts/sts-with-ingress.yaml"
kubectl apply -n "${ns}" -f "${repo_root}/tests/e2e-artifacts/pod-demo.yaml"

echo "Waiting for resources to become ready..."
kubectl wait -n "${ns}" --for=condition=Available deployment/deployment-demo --timeout=2m
kubectl wait -n "${ns}" --for=jsonpath='{.status.readyReplicas}'=1 statefulset/sts-with-ingress --timeout=2m
# pod-demo's readinessProbe is deliberately broken (see tests/e2e-artifacts/pod-demo.yaml),
# so the Deployment never reports condition=Available; wait for the container to
# actually be running instead.
kubectl wait -n "${ns}" --for=jsonpath='{.status.phase}'=Running pod -l app=pod-demo --timeout=2m
kubectl wait -n "${ns}" --for=jsonpath='{.status.phase}'=Bound pvc/pod-demo-data --timeout=2m

echo "Triggering a second Deployment revision for the rollout/diff screenshot..."
kubectl set image -n "${ns}" deployment/deployment-demo nginx=nginx:1.28
kubectl rollout status -n "${ns}" deployment/deployment-demo --timeout=2m

pod_name="$(kubectl get pods -n "${ns}" -l app=pod-demo -o jsonpath='{.items[0].metadata.name}')"
node_name="$(kubectl get pod -n "${ns}" "${pod_name}" -o jsonpath='{.spec.nodeName}')"

echo "Waiting for the metrics-server to report pod resource usage (can take a couple minutes to warm up)..."
metrics_deadline=$((SECONDS + 180))
until kubectl top pod -n "${ns}" "${pod_name}" >/dev/null 2>&1; do
  if [ "${SECONDS}" -ge "${metrics_deadline}" ]; then
    echo "Timed out waiting for metrics-server; continuing without resource usage data." >&2
    break
  fi
  sleep 5
done

echo "Injecting a made-up RebootScheduled condition onto Node/${node_name}..."
# Strategic merge (not JSON-patch "add") so NodeStatus.Conditions' "type" merge
# key updates our condition in place on repeat runs instead of appending a
# duplicate entry every time.
now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
kubectl patch node "${node_name}" --subresource=status --type=strategic -p "$(cat <<EOF
{"status": {"conditions": [{
  "type": "RebootScheduled", "status": "True",
  "reason": "Maintenance", "message": "Node has upcoming scheduled maintenance",
  "lastHeartbeatTime": "${now}", "lastTransitionTime": "${now}"
}]}}
EOF
)"

shot() {
  local out="$1"
  shift
  echo "Rendering assets/${out}..."
  # freeze falls back to reading code from stdin when it isn't a tty, which
  # clobbers --execute if stdin is a pipe (e.g. piped confirmation input above).
  # --wrap keeps images narrow enough to stay legible after GitHub's README
  # image scaling (kubectl-status doesn't wrap its own long lines).
  # Note: combining --wrap with a custom --font.size has been observed to
  # under-calculate the image height and silently crop the last wrapped
  # line(s) -- so --wrap is used here at freeze's default font size only.
  # freeze scales user-supplied padding 4x for auto-sized PNG output and adds a
  # fixed offset on top of that for --window's traffic-light dots, so even a
  # small --padding value leaves a visible gap above/below the text; 0 for
  # top/bottom keeps that gap to the unavoidable minimum.
  freeze --execute "${bin} $* --color always" \
    --window --show-line-numbers=false --font.family "${font_family}" --wrap 120 \
    --padding 0,10,0,10 \
    -o "${assets}/${out}" </dev/null
}

shot pod.png pod "${pod_name}" -n "${ns}" --include-owners --include-events=false
shot deployment-replicaset.png deployment deployment-demo -n "${ns}" --include-rollout-diffs
shot statefulset.png statefulset sts-with-ingress -n "${ns}"
shot service.png service sts-with-ingress -n "${ns}"

echo "Done. Review the updated images under assets/ before committing."
