#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Bring up the shared store T4 needs (SPEC §19.4) and leave behind the two
# things the chart asks for: a resolvable connection string and a Secret
# holding the credentials.
#
# The steps mirror what the E2E runner does for T2 and T3 in
# test/e2e/runner/couchbase.go, and for the same reasons — the bucket is
# created here because creating one needs cluster-manager rights that mockulus
# deliberately does not ask for, while the scope, the collections and the
# journal index are left to `manage_bucket` at boot, which is the zero-config
# path of SPEC §7.2 and therefore the path worth testing.
#
# Fixture credentials, not secrets: the cluster lives inside a kind node for
# the length of one job and is never published. Couchbase rejects passwords
# under six characters, which is the only constraint on the value.
set -euo pipefail

MANIFEST=${MANIFEST:-test/e2e/topologies/kind-couchbase.yaml}
POD=${POD:-couchbase-0}
SECRET=${SECRET:-mockulus-couchbase}
CB_USER=${CB_USER:-Administrator}
CB_PASSWORD=${CB_PASSWORD:-t4-couchbase-pw}
CB_BUCKET=${CB_BUCKET:-mockulus}

# The name the node advertises and the SDK therefore dials. It has to be the
# one the headless Service publishes, or a client that connected successfully
# is handed a cluster map it cannot reach.
NAMESPACE=$(kubectl config view --minify -o jsonpath='{..namespace}' 2>/dev/null || true)
NAMESPACE=${NAMESPACE:-default}
FQDN="${POD}.couchbase.${NAMESPACE}.svc.cluster.local"

# Every subcommand is addressed at the node's own loopback, the way the T2/T3
# harness does it: the cluster address is a required argument, and talking to
# 127.0.0.1 from inside the pod means none of this depends on the Service, the
# advertised hostname, or on the node being initialised yet — which matters,
# because node-init is what makes the advertised hostname resolvable in the
# first place.
cli() { kubectl exec "$POD" -- couchbase-cli "$@" -c 127.0.0.1; }

# Poll rather than sleep: every step below has a wide spread between "the
# previous call returned" and "the cluster will accept the next one", and a
# fixed sleep either flakes or wastes the difference on every run.
poll() {
  local what=$1 deadline=$2; shift 2
  local end=$(( $(date +%s) + deadline ))
  until "$@" >/dev/null 2>&1; do
    if [ "$(date +%s)" -ge "$end" ]; then
      echo "couchbase: $what (gave up after ${deadline}s)" >&2
      kubectl logs "$POD" --tail=50 >&2 || true
      return 1
    fi
    sleep 3
  done
}

echo "couchbase: applying $MANIFEST"
kubectl apply -f "$MANIFEST"
kubectl rollout status statefulset/couchbase --timeout=300s

echo "couchbase: initialising the node as $FQDN"
poll "the node never accepted node-init" 120 \
  cli node-init --node-init-hostname "$FQDN"

echo "couchbase: cluster-init"
poll "the cluster never accepted cluster-init" 180 \
  cli cluster-init \
    --cluster-username "$CB_USER" \
    --cluster-password "$CB_PASSWORD" \
    --services data,index,query \
    --cluster-ramsize 512 \
    --cluster-index-ramsize 512

echo "couchbase: creating the $CB_BUCKET bucket"
poll "the cluster never accepted the bucket creation" 120 \
  cli bucket-create \
    -u "$CB_USER" -p "$CB_PASSWORD" \
    --bucket "$CB_BUCKET" \
    --bucket-type couchbase \
    --bucket-ramsize 256 \
    --wait

# "The bucket exists" and "a client can use it" are a long way apart, and the
# gap is where a suite that fails one run in twenty comes from. Both services
# are checked because mockulus needs both: KV for the stubs, query for the DDL
# it runs at boot.
echo "couchbase: waiting for the bucket to be healthy for KV"
poll "the bucket's nodes never became healthy" 180 \
  kubectl exec "$POD" -- curl -sf -u "$CB_USER:$CB_PASSWORD" \
    "http://127.0.0.1:8091/pools/default/buckets/$CB_BUCKET"

echo "couchbase: waiting for the query service to see it"
poll "the query service never saw the bucket" 180 \
  kubectl exec "$POD" -- curl -sf -u "$CB_USER:$CB_PASSWORD" \
    http://127.0.0.1:8093/query/service \
    -d "statement=SELECT RAW 1 FROM system:keyspaces WHERE name = '$CB_BUCKET'"

echo "couchbase: writing the $SECRET secret"
kubectl delete secret "$SECRET" --ignore-not-found >/dev/null
kubectl create secret generic "$SECRET" \
  --from-literal=username="$CB_USER" \
  --from-literal=password="$CB_PASSWORD"

echo "couchbase: ready at couchbase://$FQDN"
