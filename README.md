# trafficctl

A Kubernetes controller that manages traffic weights on Gateway API
`HTTPRoute` backends from declarative policy. It reads latency and
HTTP error-rate signals from a Prometheus-compatible source, decides
weight adjustments within bounds you declared, and applies them.

![demo](hack/demo/demo.gif)

Turkish usage guide: [docs/KULLANIM_KILAVUZU.md](docs/KULLANIM_KILAVUZU.md)

## Why

You have two versions of a service behind the same `HTTPRoute` —
`auth-v1` (stable) and `auth-v2` (canary). You want v2 to take more
traffic if it stays healthy, and back off automatically if its p95
latency or 5xx rate breaches a threshold. trafficctl is the loop that
watches the metrics and patches the route weights for you, with
declarative bounds (`minWeight`/`maxWeight`), per-step caps, and a
cooldown.

It is **not** a replacement for Argo Rollouts or Flagger — see the
"How this compares" note at the bottom. It is a focused, kubebuilder-
native take on the same problem.

## Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│                  SENSE  →  DECIDE  →  APPLY                       │
│                                                                   │
│   metrics.Source     evaluator.Evaluator     router.Weighter      │
│   (Prometheus,       (+Smoother)             (HTTPRoute patch)    │
│    static, ...)                                                   │
└──────────────────────────────────────────────────────────────────┘
                            ▲
                            │
                  TrafficPolicy CRD ─── reconciler
                            ▲
                            │
                trafficctl CLI (list/status/freeze/resume)
```

## Quickstart on Kind

Bring up a Kind cluster pre-wired for the demo:

```sh
hack/kind/setup.sh
```

This creates a cluster, installs Gateway API CRDs, builds the
controller image, side-loads it, installs the TrafficPolicy CRD, and
deploys the manager pointed at an in-cluster fake Prometheus.

Then walk the end-to-end scenario:

```sh
make build-cli
DEMO_AUTO=1 hack/demo/demo.sh
```

What the demo does:

1. Deploys two echo Services (`echo-v1`, `echo-v2`) and an
   `HTTPRoute` that splits across them.
2. Applies a `TrafficPolicy` with a 500ms latency threshold.
3. Swaps a ConfigMap to simulate `echo-v2` breaching the threshold.
4. Watches the controller shift weight away from v2, recording an
   `Event` and bumping `trafficctl_weight_shifts_total`.
5. Freezes/resumes the policy via the `trafficctl` CLI.

To record a GIF of this flow (requires [`vhs`](https://github.com/charmbracelet/vhs)):

```sh
vhs hack/demo/demo.tape   # → hack/demo/demo.gif
```

## CRD: TrafficPolicy

```yaml
apiVersion: traffic.traffic.devops.io/v1alpha1
kind: TrafficPolicy
metadata: { name: auth-api, namespace: default }
spec:
  routeName: auth-route
  backends:
    - { name: auth-v1, minWeight: 20, maxWeight: 100 }
    - { name: auth-v2, minWeight: 10, maxWeight: 80 }
  metrics:
    latency:
      query: 'histogram_quantile(0.95, rate(http_request_duration_seconds_bucket{service="auth"}[1m]))'
      thresholdMs: 500
    errorRate:
      query: '100 * sum by (backend) (rate(http_requests_total{status=~"5.."}[1m])) / sum by (backend) (rate(http_requests_total[1m]))'
      thresholdPercent: 2
  strategy:
    cooldownSeconds: 30
    maxStepPercent: 20
    movingAverageWindow: 3
```

A backend is **unhealthy** when *any* configured signal exceeds its
threshold. The controller shifts up to `maxStepPercent` percentage
points per backend per decision, never violating each backend's
`minWeight`/`maxWeight`. If the metrics source is unavailable, weights
are **frozen** (fail-safe — better to hold than to shift on bad data).

## CLI

```sh
trafficctl list [-A]              # list policies, show phase + weights
trafficctl status NAME            # detailed view (bounds, current weights, conditions)
trafficctl freeze NAME            # set .spec.paused=true
trafficctl resume NAME            # set .spec.paused=false
```

Build with `make build-cli` → `bin/trafficctl`.

## Observability

The controller serves Prometheus-format metrics on
`--metrics-bind-address` (default `:8443`, HTTPS). Two layers:

**Framework metrics** (provided by controller-runtime):

- `controller_runtime_reconcile_total{controller,result}`
- `controller_runtime_reconcile_errors_total{controller}`
- `controller_runtime_reconcile_time_seconds{controller}`

**Domain metrics** (emitted by trafficctl):

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `trafficctl_weight_shifts_total` | counter | `namespace`, `name` | Total weight changes applied per policy. |
| `trafficctl_freezes_total` | counter | `namespace`, `name`, `cause` | Reconciles that held weights, by cause: `paused`, `metrics_unavailable`, `route_missing`, `invalid_spec`. |
| `trafficctl_metric_source_errors_total` | counter | `namespace`, `name`, `signal` | Per-signal failures from the metrics source. |
| `trafficctl_backend_weight` | gauge | `namespace`, `name`, `backend` | Currently applied weight (0–100) per backend. |
| `trafficctl_policy_phase` | gauge | `namespace`, `name`, `phase` | One-hot: `1` for the current phase, `0` for the others. |

When a `TrafficPolicy` is deleted, gauge series for that policy are
cleared via `DeletePartialMatch`. Counters are kept so `rate()` is
still meaningful across the deletion boundary.

A starter PromQL panel set:

```promql
# Shifts per minute, by policy
sum by (namespace, name) (rate(trafficctl_weight_shifts_total[5m]))

# How often each policy is freezing, by cause
sum by (namespace, name, cause) (rate(trafficctl_freezes_total[5m]))

# Current backend weights
trafficctl_backend_weight

# Time spent in each phase
avg_over_time(trafficctl_policy_phase[5m])
```

## Events

The controller emits Kubernetes `Event`s on each state transition,
attached to the `TrafficPolicy` object. View them with
`kubectl describe tp NAME` or `kubectl get events`.

| Reason | Type | Emitted when |
|---|---|---|
| `WeightsShifted` | Normal | A reconcile changed the applied weights. The message includes the new allocation and which signals drove it. |
| `Paused` | Normal | `.spec.paused` flipped from false to true. |
| `Resumed` | Normal | `.spec.paused` flipped from true to false. |
| `MetricsUnavailable` | Warning | The metrics source returned an error and `MetricsAvailable` transitioned to `False`. Re-emitted only on the *transition*, not on every failed scrape. |
| `RouteMissing` | Warning | The target `HTTPRoute` was not found and the policy entered `Degraded`. |
| `RouteUpdateFailed` | Warning | Patching the `HTTPRoute` failed for a reason other than not-found. |
| `InvalidSpec` | Warning | Validation rejected the spec on transition into `Degraded`. |

Events are emitted only on transitions (e.g. on entry into `Degraded`,
not every reconcile while degraded). This keeps `kubectl describe`
readable and avoids the event-recorder spam pattern that makes operator
Events useless on real clusters.

Example after a latency-driven shift:

```
$ kubectl describe tp echo-canary
Events:
  Type    Reason          Age   From                       Message
  ----    ------          ----  ----                       -------
  Normal  WeightsShifted  18s   trafficpolicy-controller   Shifted 20 pp: echo-v2(latency) -> echo-v1 (new weights: echo-v1=70,echo-v2=30)
```

## Deploy on a real cluster

```sh
make docker-build docker-push IMG=<some-registry>/trafficctl:tag
make install
make deploy IMG=<some-registry>/trafficctl:tag --prometheus-address=http://prometheus.monitoring:9090
```

Manager flags:

- `--prometheus-address` — base URL of the Prometheus HTTP API. Empty means no metric-driven shifts (controller still enforces bounds and honors `paused`).
- `--prometheus-backend-label` — label that identifies the backend in query results. Default: `backend`.
- `--prometheus-timeout` — HTTP timeout per query. Default: `5s`.

Sample CR:

```sh
kubectl apply -k config/samples/
```

Uninstall:

```sh
kubectl delete -k config/samples/
make undeploy
make uninstall
```

## Live Test Result

This project was exercised on a real Azure Kubernetes Service (AKS)
cluster, not only in `envtest` or Kind.

The live setup included:

- `trafficctl` controller deployed as a Kubernetes `Deployment`
- `TrafficPolicy` CRD installed in the cluster
- a real Gateway API implementation (`NGINX Gateway Fabric`)
- a live `Gateway` with a public IP
- a live `HTTPRoute` splitting traffic between two backend Services
- a Prometheus-compatible metrics source wired into the controller

### Test scenario

Two backend versions were deployed behind the same `HTTPRoute`:

- `echo-v1`
- `echo-v2`

Initial route weights were:

```yaml
echo-v1: 50
echo-v2: 50
```

Traffic sent through the Gateway showed the expected near-even split:

```text
51 hello from v1
49 hello from v2
```

Then the metrics source was changed to simulate degraded latency on
`echo-v2`. The controller reacted in bounded steps according to the
declared policy:

```text
70/30
90/10
100/0
```

The final live `TrafficPolicy` status showed:

```yaml
status:
  phase: Stable
  weights:
    - name: echo-v1
      weight: 100
    - name: echo-v2
      weight: 0
```

The final live `HTTPRoute` also reflected the controller's decision:

```yaml
spec:
  rules:
    - backendRefs:
        - name: echo-v1
          port: 80
          weight: 100
        - name: echo-v2
          port: 80
          weight: 0
```

After the shift, repeated real HTTP requests through the public Gateway
address produced:

```text
100 hello from v1
```

This verifies that `trafficctl` did not just update CR status: it
successfully changed live Gateway traffic behavior on a real Kubernetes
cluster.

## How this compares

[Argo Rollouts](https://argoproj.github.io/rollouts/) and
[Flagger](https://flagger.app/) are mature, well-supported
projects that solve a superset of this problem (analysis with
statistical significance, baseline comparison, webhook hooks,
notification integrations, GitOps-shaped flows).

trafficctl is intentionally narrower:

- **Single CRD, narrow surface.** Just `TrafficPolicy` and Gateway API
  `HTTPRoute`. No Rollout/Canary objects, no analysis templates.
- **Threshold-based, not statistical.** Deliberately simple. Easy to
  reason about; not appropriate for low-signal canaries.
- **Fail-safe by default.** Metrics down ⇒ freeze. No promotion logic.

Use trafficctl if you want a small, auditable building block. Use
Flagger or Argo Rollouts if you need the deeper analysis surface.

## Development

```sh
make manifests generate fmt vet test   # the usual
make build      # bin/manager
make build-cli  # bin/trafficctl
```

Test layout:

- `internal/evaluator` — pure-function evaluator + smoother (unit).
- `internal/metrics` — `Source` interface, static + Prometheus impls (`httptest` round-trips).
- `internal/router` — `Weighter` over Gateway API `HTTPRoute` (controller-runtime fake client).
- `internal/controller` — reconciler, observability, events (`envtest` integration + `FakeRecorder`).
- `internal/cli` — Cobra-based operator CLI (fake client).

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0. See the LICENSE
boilerplate in source files for details.
