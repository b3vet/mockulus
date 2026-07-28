#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Graceful-drain check: mock traffic must not see a single 5xx or a dropped
# connection while every pod is replaced. This is what the readiness flip,
# the drain window and the preStop sleep of SPEC §4.5 exist to buy, so it is
# worth asserting rather than assuming.

set -euo pipefail

RELEASE="${1:-mockulus}"
DURATION="${2:-45}"

kubectl run "rollout-stub-$RANDOM" \
  --image=curlimages/curl:8.11.1 --restart=Never --rm -i --quiet \
  --command -- sh -euc "
    curl -sf -X POST \"http://$RELEASE:8080/__admin/mappings\" \
      -H 'Content-Type: application/json' \
      -d '{\"persistent\":true,\"request\":{\"method\":\"GET\",\"urlPath\":\"/kind/rollout\"},\"response\":{\"status\":200,\"body\":\"up\"}}' \
      >/dev/null
  "

# Drive traffic from inside the cluster for the whole rollout.
kubectl run rollout-load \
  --image=curlimages/curl:8.11.1 --restart=Never \
  --command -- sh -euc "
    bad=0; total=0
    end=\$(( \$(date +%s) + $DURATION ))
    while [ \$(date +%s) -lt \$end ]; do
      code=\$(curl -s -m 5 -o /dev/null -w '%{http_code}' \"http://$RELEASE:8080/kind/rollout\" || echo 000)
      total=\$((total+1))
      case \"\$code\" in 200) ;; *) bad=\$((bad+1)); echo \"bad response: \$code\" ;; esac
      sleep 0.05
    done
    echo \"RESULT total=\$total bad=\$bad\"
    [ \"\$bad\" -eq 0 ]
  "

sleep 3
kubectl rollout restart "deployment/$RELEASE"
kubectl rollout status "deployment/$RELEASE" --timeout=180s

kubectl wait --for=jsonpath='{.status.phase}'=Succeeded pod/rollout-load --timeout=120s || true
kubectl logs rollout-load | tail -20
status=$(kubectl get pod rollout-load -o jsonpath='{.status.phase}')
kubectl delete pod rollout-load --wait=false >/dev/null

if [ "$status" != "Succeeded" ]; then
  echo "requests failed during the rolling restart" >&2
  exit 1
fi
echo "rollout drain check passed: no failed requests"
