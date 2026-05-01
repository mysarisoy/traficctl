#!/usr/bin/env bash
# demo.sh — uçtan uca trafficctl demosu (Kind üzerinde).
#
# Önce hack/kind/setup.sh çalıştırılmış olmalı.
# Senaryo:
#   1. fake-prom + echo deployments + HTTPRoute + TrafficPolicy
#   2. Sağlıklı baseline → weights stable
#   3. fake-prom yanıtını bozarak v2 latency'sini eşik üstüne çıkar
#   4. Controller weight'leri kaydırır → events + metrics
#   5. trafficctl freeze ile durdur
#   6. trafficctl resume ile devam ettir
#
# Komutlar arasında "press to continue" prompt'ları var; CI/non-interactive
# kullanım için DEMO_AUTO=1 ile geç.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TRAFFICCTL="${TRAFFICCTL:-$ROOT/bin/trafficctl}"
NS=trafficctl-demo

pause() {
  if [[ "${DEMO_AUTO:-0}" == "1" ]]; then
    sleep "${DEMO_PAUSE:-2}"
  else
    read -r -p "press enter to continue..."
  fi
}

step() { printf "\n\033[1;36m==> %s\033[0m\n" "$*"; }

step "Apply fake-prom + echo app + HTTPRoute"
kubectl apply -f "$ROOT/hack/demo/fake-prom.yaml"
kubectl apply -f "$ROOT/hack/demo/echo-route.yaml"
kubectl -n "$NS" rollout status deploy/fake-prom --timeout=60s
kubectl -n "$NS" rollout status deploy/echo-v1   --timeout=60s
kubectl -n "$NS" rollout status deploy/echo-v2   --timeout=60s
pause

step "Apply TrafficPolicy"
kubectl apply -f "$ROOT/hack/demo/policy.yaml"
sleep 3
"$TRAFFICCTL" -n "$NS" status echo-canary
pause

step "HTTPRoute weights — controller has computed an initial allocation"
kubectl -n "$NS" get httproute echo-route -o jsonpath='{.spec.rules[0].backendRefs[*].weight}{"\n"}'
pause

step "Simulate v2 latency spike — swap fake-prom payload"
kubectl apply -f "$ROOT/hack/demo/fake-prom-degraded.yaml"
kubectl -n "$NS" rollout restart deploy/fake-prom
kubectl -n "$NS" rollout status  deploy/fake-prom --timeout=30s
pause

step "Wait for controller to react (cooldown=10s, requeue catches it)"
sleep 15
"$TRAFFICCTL" -n "$NS" status echo-canary
pause

step "Watch the events the controller emitted"
kubectl -n "$NS" describe trafficpolicy echo-canary | sed -n '/Events:/,$p'
pause

step "Controller's own /metrics surface"
# The metrics endpoint is auth-protected by default. Bind metrics-reader
# to the manager's SA (idempotent) and scrape with its token.
kubectl create clusterrolebinding demo-metrics-reader \
  --clusterrole=trafficctl-metrics-reader \
  --serviceaccount=trafficctl-system:trafficctl-controller-manager \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
TOKEN="$(kubectl -n trafficctl-system create token trafficctl-controller-manager)"
( kubectl -n trafficctl-system port-forward svc/trafficctl-controller-manager-metrics-service 8443:8443 >/dev/null 2>&1 ) &
PF_PID=$!
sleep 2
curl -sk -H "Authorization: Bearer $TOKEN" https://localhost:8443/metrics 2>/dev/null \
  | grep -E '^trafficctl_' | head -15 \
  || echo "(metrics scrape failed)"
kill "$PF_PID" 2>/dev/null
wait "$PF_PID" 2>/dev/null || true
pause

step "Freeze the policy via trafficctl"
"$TRAFFICCTL" -n "$NS" freeze echo-canary
sleep 3
"$TRAFFICCTL" -n "$NS" status echo-canary | grep -E 'Phase|Paused'
pause

step "Resume"
"$TRAFFICCTL" -n "$NS" resume echo-canary
sleep 3
"$TRAFFICCTL" -n "$NS" status echo-canary | grep -E 'Phase|Paused'

step "Demo done."
