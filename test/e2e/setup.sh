#!/usr/bin/env bash
# Prepares a kind/k3d cluster (1 control plane + 3 workers) for the e2e tests:
# site labels, sample workloads, and a kubeconfig that only has the read-only RBAC role.
#
#   kind create cluster --name edge --config test/e2e/kind.yaml
#   test/e2e/setup.sh
#   make e2e
set -euo pipefail
cd "$(dirname "$0")/../.."
out="${E2E_DIR:-test/e2e/.out}"
mkdir -p "$out"

mapfile -t workers < <(kubectl get nodes -l '!node-role.kubernetes.io/control-plane' -o name | sed 's|node/||' | sort)
[ "${#workers[@]}" -ge 3 ] || { echo "need at least 3 worker nodes, found ${#workers[@]}" >&2; exit 1; }

# Two nodes at one site, one alone at another. Each test workload is pinned to its own
# node, so every drain scenario (PDB block, emptyDir refusal, clean drain) is deterministic.
kubectl label node "${workers[0]}" topology.kubernetes.io/zone=store-a e2e/pool=web --overwrite
kubectl label node "${workers[1]}" topology.kubernetes.io/zone=store-a e2e/cache=true --overwrite
kubectl label node "${workers[2]}" topology.kubernetes.io/zone=store-b e2e/solo=true --overwrite

kubectl apply -f test/e2e/manifests.yaml
kubectl -n e2e rollout status deploy/web deploy/cache deploy/solo --timeout=240s

# Read-only identity: the service account from deploy/rbac-readonly.yaml, nothing else.
kubectl apply -f deploy/rbac-readonly.yaml
token="$(kubectl -n edge-mcp create token edge-mcp --duration=2h)"
server="$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d > "$out/ca.crt"
ro="$out/readonly.kubeconfig"
rm -f "$ro"
kubectl --kubeconfig "$ro" config set-cluster edge --server "$server" --certificate-authority "$out/ca.crt" --embed-certs=true >/dev/null
kubectl --kubeconfig "$ro" config set-credentials edge-mcp --token "$token" >/dev/null
kubectl --kubeconfig "$ro" config set-context edge --cluster edge --user edge-mcp >/dev/null
kubectl --kubeconfig "$ro" config use-context edge >/dev/null
echo "workers: ${workers[*]}"
echo "read-only kubeconfig: $ro"
