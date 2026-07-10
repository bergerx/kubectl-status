#!/usr/bin/env bash
# Regenerates the demo screenshots in assets/*.png from a live cluster.
#
# Requires:
#   - kubectl pointed at a disposable/dev cluster (e.g. minikube) -- this script
#     creates and deletes its own namespace, but does not touch any other
#     namespace or cluster-scoped state, EXCEPT: it installs the Gateway API
#     CRDs and cert-manager cluster-wide if they aren't already present (needed
#     for the HTTPRoute/TCPRoute and cert-manager-issued-Secret screenshots),
#     and removes them again in cleanup if this run is what installed them. Set
#     ASSUME_KUBECONFIG_IS_DISPOSABLE=true to skip the confirmation prompt (e.g.
#     in CI).
#   - freeze (https://github.com/charmbracelet/freeze): go install github.com/charmbracelet/freeze@latest
#
# freeze renders with the "JetBrains Mono" font and exits 0 even when that font
# isn't installed, silently producing tofu-box images instead of text. This
# script registers it into a user fontconfig dir (no root needed) if missing.
set -euo pipefail

font_family="JetBrains Mono"
# shellcheck source=hack/versions.env
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/versions.env"
gateway_api_version="${GATEWAY_API_VERSION}"
cert_manager_version="${CERT_MANAGER_VERSION}"

if [ "$(uname)" = "Darwin" ]; then
  # macOS has no fontconfig; dropping a .ttf into ~/Library/Fonts is enough,
  # no cache step needed.
  font_dir="${HOME}/Library/Fonts"
  if ! find /System/Library/Fonts /Library/Fonts "${font_dir}" -iname "JetBrainsMono-Regular.ttf" 2>/dev/null | grep -q .; then
    echo "'${font_family}' not found; downloading it for the current user..."
    mkdir -p "${font_dir}"
    curl -sSL -o "${font_dir}/JetBrainsMono-Regular.ttf" \
      "https://github.com/JetBrains/JetBrainsMono/raw/master/fonts/ttf/JetBrainsMono-Regular.ttf"
  fi
elif command -v fc-list >/dev/null 2>&1; then
  if ! fc-list 2>/dev/null | grep -qi "jetbrains mono"; then
    echo "'${font_family}' not found by fontconfig; downloading and registering it for the current user..."
    font_dir="${HOME}/.local/share/fonts"
    mkdir -p "${font_dir}"
    curl -sSL -o "${font_dir}/JetBrainsMono-Regular.ttf" \
      "https://github.com/JetBrains/JetBrainsMono/raw/master/fonts/ttf/JetBrainsMono-Regular.ttf"
    if command -v fc-cache >/dev/null 2>&1; then
      fc-cache -f "${font_dir}"
    fi
    if ! fc-list 2>/dev/null | grep -qi "jetbrains mono"; then
      echo "Failed to register '${font_family}' with fontconfig; freeze would silently render blank/tofu images. Aborting." >&2
      exit 1
    fi
  fi
else
  echo "fontconfig (fc-list) not found; skipping the font check -- freeze will silently render tofu boxes instead of text if '${font_family}' isn't already installed." >&2
fi

context="$(kubectl config current-context)"

is_minikube=false
metrics_server_already_enabled=false
if command -v minikube >/dev/null 2>&1 && [ "${context}" = "minikube" ]; then
  is_minikube=true
  if minikube addons list 2>/dev/null | grep -q "metrics-server.*enabled"; then
    metrics_server_already_enabled=true
  fi
fi

gateway_api_already_installed=false
if kubectl get crd httproutes.gateway.networking.k8s.io >/dev/null 2>&1; then
  gateway_api_already_installed=true
fi

cert_manager_already_installed=false
if kubectl get crd certificates.cert-manager.io >/dev/null 2>&1; then
  cert_manager_already_installed=true
fi

if [ "${ASSUME_KUBECONFIG_IS_DISPOSABLE:-}" != "true" ]; then
  cat <<EOF
This will, on kubectl context '${context}':
  - create and delete a namespace
EOF
  if [ "${is_minikube}" = "true" ] && [ "${metrics_server_already_enabled}" = "false" ]; then
    cat <<EOF
  - enable the minikube 'metrics-server' addon, then disable it again when done
    (cluster-wide, affects other workloads using it in the meantime)
EOF
  fi
  if [ "${gateway_api_already_installed}" = "false" ]; then
    cat <<EOF
  - install the Gateway API CRDs (${gateway_api_version}), then remove them again when done
    (cluster-wide, needed for the HTTPRoute/TCPRoute-on-Service screenshot)
EOF
  fi
  if [ "${cert_manager_already_installed}" = "false" ]; then
    cat <<EOF
  - install cert-manager (${cert_manager_version}) into its own 'cert-manager' namespace, then
    remove it again when done (cluster-wide, needed for the cert-manager-issued-Secret screenshot)
EOF
  fi
  cat <<EOF
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
ns="ks-demo"
tmp_dir="$(mktemp -d)"
bin="${tmp_dir}/kubectl-status"
assets="${repo_root}/assets"

echo "Building kubectl-status..."
go build -o "${bin}" "${repo_root}/cmd"

node_name=""

cleanup() {
  rm -rf "${tmp_dir}"
  if [ -n "${node_name}" ]; then
    echo "Removing the injected RebootScheduled condition from Node/${node_name}..."
    # $patch: delete is the strategic-merge-patch directive for removing a
    # list item by its merge key ("type"), rather than by index.
    kubectl patch node "${node_name}" --subresource=status --type=strategic -p \
      '{"status": {"conditions": [{"$patch": "delete", "type": "RebootScheduled"}]}}' \
      >/dev/null 2>&1 || true
  fi
  if [ "${is_minikube}" = "true" ] && [ "${metrics_server_already_enabled}" = "false" ]; then
    echo "Disabling metrics-server addon..."
    minikube addons disable metrics-server >/dev/null 2>&1 || true
  fi
  echo "Deleting namespace ${ns}..."
  kubectl delete namespace "${ns}" --ignore-not-found --wait=false >/dev/null
  if [ "${cert_manager_already_installed}" = "false" ]; then
    echo "Removing cert-manager..."
    kubectl delete -f "https://github.com/cert-manager/cert-manager/releases/download/${cert_manager_version}/cert-manager.yaml" \
      --ignore-not-found --wait=false >/dev/null 2>&1 || true
  fi
  if [ "${gateway_api_already_installed}" = "false" ]; then
    echo "Removing Gateway API CRDs..."
    kubectl delete -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/${gateway_api_version}/experimental-install.yaml" \
      --ignore-not-found --wait=false >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

if [ "${is_minikube}" = "true" ] && [ "${metrics_server_already_enabled}" = "false" ]; then
  echo "Enabling minikube metrics-server addon..."
  minikube addons enable metrics-server >/dev/null
elif [ "${is_minikube}" = "true" ]; then
  echo "minikube metrics-server addon is already enabled; leaving it as-is."
else
  echo "Not running against minikube (context: ${context}); skipping metrics-server addon management. Resource-usage data in the pod screenshot will only appear if metrics-server is already installed." >&2
fi

if [ "${gateway_api_already_installed}" = "false" ]; then
  echo "Installing Gateway API CRDs (${gateway_api_version}, experimental channel)..."
  # --server-side: these CRDs are too large for the client-side last-applied-configuration
  # annotation ("metadata.annotations: Too long"); server-side apply is upstream's documented
  # workaround.
  kubectl apply --server-side -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/${gateway_api_version}/experimental-install.yaml" >/dev/null
else
  echo "Gateway API CRDs already installed; leaving them as-is."
fi

if [ "${cert_manager_already_installed}" = "false" ]; then
  echo "Installing cert-manager (${cert_manager_version})..."
  kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${cert_manager_version}/cert-manager.yaml" >/dev/null
  echo "Waiting for cert-manager to become ready..."
  kubectl wait -n cert-manager --for=condition=Available deployment --all --timeout=2m
else
  echo "cert-manager already installed; leaving it as-is."
fi

echo "Creating namespace ${ns}..."
kubectl create namespace "${ns}"

echo "Applying demo manifests..."
kubectl apply -n "${ns}" -f "${repo_root}/tests/e2e-artifacts/web.yaml"
kubectl apply -n "${ns}" -f "${repo_root}/tests/e2e-artifacts/web-policies.yaml"
kubectl apply -n "${ns}" -f "${repo_root}/tests/e2e-artifacts/web-cert.yaml"
kubectl apply -n "${ns}" -f "${repo_root}/tests/e2e-artifacts/sts-with-ingress.yaml"
kubectl apply -n "${ns}" -f "${repo_root}/tests/e2e-artifacts/sts-with-ingress-routes.yaml"
kubectl apply -n "${ns}" -f "${repo_root}/tests/e2e-artifacts/pod-demo.yaml"

echo "Waiting for resources to become ready..."
kubectl wait -n "${ns}" --for=condition=Available deployment/web --timeout=2m
kubectl wait -n "${ns}" --for=jsonpath='{.status.readyReplicas}'=1 statefulset/sts-with-ingress --timeout=2m
kubectl wait -n "${ns}" --for=condition=Ready certificate/web-tls --timeout=2m
# pod-demo's readinessProbe is deliberately broken (see tests/e2e-artifacts/pod-demo.yaml),
# so the Deployment never reports condition=Available; wait for the container to
# actually be running instead.
kubectl wait -n "${ns}" --for=jsonpath='{.status.phase}'=Running pod -l app=pod-demo --timeout=2m
kubectl wait -n "${ns}" --for=jsonpath='{.status.phase}'=Bound pvc/pod-demo-data --timeout=2m

echo "Triggering a failing second Deployment revision (bad image tag) for the rollout/diff screenshot..."
# A nonexistent tag makes the new ReplicaSet's Pods stick in ImagePullBackOff, so the rollout
# never completes and kubectl-status auto-shows the diff between revisions without needing
# --include-rollout-diffs. Deliberately not using `kubectl rollout status` here -- it would just
# time out and, under `set -euo pipefail`, abort the script.
kubectl set image -n "${ns}" deployment/web nginx=nginx:this-tag-does-not-exist
echo "Waiting for the new revision's Pods to report an image pull failure..."
rollout_deadline=$((SECONDS + 60))
until kubectl get pods -n "${ns}" -l app=web \
    -o jsonpath='{.items[*].status.containerStatuses[*].state.waiting.reason}' 2>/dev/null \
    | grep -qE 'ErrImagePull|ImagePullBackOff'; do
  if [ "${SECONDS}" -ge "${rollout_deadline}" ]; then
    echo "Timed out waiting for the image pull failure; continuing anyway." >&2
    break
  fi
  sleep 2
done

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
  go run github.com/charmbracelet/freeze@latest --execute "${bin} $* --color always" \
    --window --show-line-numbers=false --font.family "${font_family}" --wrap 120 \
    --padding 0,10,0,10 \
    -o "${assets}/${out}" </dev/null
}

shot pod.png pod "${pod_name}" -n "${ns}" --include-owners --include-events=false
shot deployment-replicaset.png deployment web -n "${ns}"
shot statefulset.png statefulset sts-with-ingress -n "${ns}"
shot service.png service sts-with-ingress -n "${ns}"
shot secret.png secret web-tls -n "${ns}"

echo "Done. Review the updated images under assets/ before committing."
