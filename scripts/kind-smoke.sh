#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The T4 smoke of SPEC §19.4: a real Helm release in a real cluster, exercised
# only through the Service — which is what proves "any replica serves any
# request and any admin call" rather than assuming it.
#
# The stub round-trip asserts every request is served, so it only means
# something when every replica can see the stub. With the memory or file
# driver each pod keeps its own stubs (SPEC §7.1, §15.4), so that assertion is
# skipped there and reported as skipped rather than quietly weakened.

set -euo pipefail

RELEASE="${1:-mockulus}"
REQUESTS="${2:-30}"

deployment=$(kubectl get deploy "$RELEASE" -o jsonpath='{.metadata.name}')
replicas=$(kubectl get deploy "$deployment" -o jsonpath='{.status.readyReplicas}')
store=$(kubectl get deploy "$deployment" \
  -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="MOCKULUS_STORE")].value}')
connstr=$(kubectl get deploy "$deployment" \
  -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="MOCKULUS_COUCHBASE_CONNSTR")].value}')

# Resolve `auto` the way mockulus does at startup.
if [ "$store" = "auto" ]; then
  if [ -n "$connstr" ]; then store="couchbase"; else store="memory"; fi
fi

shared_store="no"
if [ "$store" = "couchbase" ]; then shared_store="yes"; fi

echo "release=$RELEASE replicas=$replicas store=$store shared=$shared_store"

# A single replica is trivially consistent with itself, so the round-trip is
# meaningful either when there is one pod or when the store is shared.
round_trip="yes"
if [ "$shared_store" = "no" ] && [ "${replicas:-1}" -gt 1 ]; then
  round_trip="no"
fi

kubectl run "kind-smoke-$RANDOM" \
  --image=curlimages/curl:8.11.1 \
  --restart=Never --rm -i --quiet \
  --env="SVC=$RELEASE" \
  --env="REQUESTS=$REQUESTS" \
  --env="ROUND_TRIP=$round_trip" \
  --command -- sh -euc '
    echo "== the ops port answers its probes"
    curl -sf "http://$SVC:9090/healthz" >/dev/null
    curl -sf "http://$SVC:9090/readyz"  >/dev/null

    echo "== the admin API answers on the mock port, for WireMock clients"
    curl -sf "http://$SVC:8080/__admin/health" | grep -q "\"status\":\"healthy\""

    echo "== the ops surface is never on the mock port"
    for path in /metrics /healthz /readyz /debug/pprof/; do
      code=$(curl -s -o /dev/null -w "%{http_code}" "http://$SVC:8080$path")
      [ "$code" = "404" ] || { echo "mock port served $path with $code"; exit 1; }
    done

    echo "== metrics are exposed on the ops port"
    curl -sf "http://$SVC:9090/metrics" | grep -q "^mockulus_build_info"

    if [ "$ROUND_TRIP" != "yes" ]; then
      echo "== stub round-trip SKIPPED: several replicas over a per-pod store"
      echo "kind smoke passed (round-trip skipped)"
      exit 0
    fi

    echo "== register a stub through the Service"
    curl -sf -X POST "http://$SVC:8080/__admin/mappings" \
      -H "Content-Type: application/json" \
      -d "{\"request\":{\"method\":\"GET\",\"urlPath\":\"/kind/smoke\"},\"response\":{\"status\":200,\"body\":\"served\"}}" \
      >/dev/null

    echo "== every request through the Service is served"
    ok=0
    i=0
    while [ "$i" -lt "$REQUESTS" ]; do
      code=$(curl -s -o /dev/null -w "%{http_code}" "http://$SVC:8080/kind/smoke")
      [ "$code" = "200" ] && ok=$((ok+1))
      i=$((i+1))
    done
    echo "200s: $ok/$REQUESTS"
    [ "$ok" -eq "$REQUESTS" ] || exit 1

    echo "kind smoke passed"
  '
