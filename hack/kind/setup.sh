#!/usr/bin/env bash
# setup.sh — bootstrap a Kind cluster wired for the trafficctl demo.
#
# What this does:
#   1. Creates a Kind cluster ("trafficctl-demo" by default).
#   2. Installs the Gateway API standard CRDs.
#   3. Builds the controller image and side-loads it into Kind.
#   4. Installs trafficctl CRDs and deploys the manager with --prometheus-address
#      pointing at the in-cluster fake-prom (see hack/demo/fake-prom.yaml).
#
# After this script finishes, run hack/demo/demo.sh to walk through the scenario.

set -euo pipefail

CLUSTER="${CLUSTER:-trafficctl-demo}"
GATEWAY_API_VERSION="${GATEWAY_API_VERSION:-v1.5.1}"
IMG="${IMG:-trafficctl:demo}"

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

step() { printf "\n\033[1;36m==> %s\033[0m\n" "$*"; }

step "Creating Kind cluster ($CLUSTER)"
if ! kind get clusters | grep -qx "$CLUSTER"; then
  kind create cluster --name "$CLUSTER" --wait 60s
else
  echo "cluster $CLUSTER already exists; reusing"
fi

step "Installing Gateway API CRDs ($GATEWAY_API_VERSION)"
kubectl apply -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION}/standard-install.yaml"

step "Building controller image ($IMG)"
make docker-build IMG="$IMG"

step "Loading image into Kind"
kind load docker-image --name "$CLUSTER" "$IMG"

step "Installing TrafficPolicy CRD"
make install

step "Deploying manager"
make deploy IMG="$IMG"

step "Patching manager to enable the demo Prometheus source"
kubectl -n trafficctl-system set env deployment/trafficctl-controller-manager \
  PROMETHEUS_ADDRESS="http://fake-prom.trafficctl-demo.svc:9090" >/dev/null 2>&1 || true
# Some kubebuilder layouts use a manager flag rather than env. Fall back to args.
kubectl -n trafficctl-system patch deployment trafficctl-controller-manager --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--prometheus-address=http://fake-prom.trafficctl-demo.svc:9090"}]' \
  >/dev/null 2>&1 || true

step "Done. Run hack/demo/demo.sh next."
