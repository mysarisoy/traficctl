# vhs tape for the trafficctl demo GIF.
# Render with: vhs hack/demo/demo.tape
# Produces: hack/demo/demo.gif (~75-90s).
#
# Prereq: hack/kind/setup.sh has been run; cluster is up; bin/trafficctl exists.

Output hack/demo/demo.gif

Set Theme        "Catppuccin Mocha"
Set FontSize     14
Set Width        1280
Set Height       720
Set Padding      20
Set TypingSpeed  35ms
Set PlaybackSpeed 1.0

# 1. Apply the demo workload
Type "kubectl apply -f hack/demo/fake-prom.yaml -f hack/demo/echo-route.yaml" Sleep 400ms Enter
Sleep 4s

# 2. Apply the policy and check status
Type "kubectl apply -f hack/demo/policy.yaml" Sleep 300ms Enter
Sleep 3s
Type "bin/trafficctl -n trafficctl-demo status echo-canary" Sleep 300ms Enter
Sleep 4s

# 3. HTTPRoute weights — controller patched them
Type "kubectl -n trafficctl-demo get httproute echo-route -o jsonpath='{range .spec.rules[0].backendRefs[*]}{.name}={.weight} {end}'; echo" Sleep 300ms Enter
Sleep 3s

# 4. Simulate v2 latency breach
Type "# v2 starts breaching 500ms threshold" Sleep 200ms Enter
Type "kubectl apply -f hack/demo/fake-prom-degraded.yaml && kubectl -n trafficctl-demo rollout restart deploy/fake-prom" Sleep 300ms Enter
Sleep 6s

# 5. Wait for cooldown + reconcile
Type "sleep 15 && bin/trafficctl -n trafficctl-demo status echo-canary" Sleep 300ms Enter
Sleep 18s

# 6. See the Events
Type "kubectl -n trafficctl-demo describe tp echo-canary | sed -n '/Events:/,$p'" Sleep 300ms Enter
Sleep 5s

# 7. Controller's metrics surface (token-auth scrape)
Type "TOKEN=$(kubectl -n trafficctl-system create token trafficctl-controller-manager)" Sleep 300ms Enter
Sleep 1s
Type "kubectl -n trafficctl-system port-forward svc/trafficctl-controller-manager-metrics-service 8443:8443 >/dev/null 2>&1 &" Sleep 300ms Enter
Sleep 2s
Type "curl -sk -H \"Authorization: Bearer $TOKEN\" https://localhost:8443/metrics | grep '^trafficctl_' | head -10" Sleep 300ms Enter
Sleep 5s

# 8. Freeze + resume
Type "bin/trafficctl -n trafficctl-demo freeze echo-canary" Sleep 300ms Enter
Sleep 2s
Type "bin/trafficctl -n trafficctl-demo resume echo-canary" Sleep 300ms Enter
Sleep 3s

Type "# done" Sleep 200ms Enter
Sleep 2s
