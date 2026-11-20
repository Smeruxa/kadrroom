set -euo pipefail

app=${1:?Expected catalog-api or frontend}
cluster=${2:?Expected the kind cluster name}
images_dir=${3:?Expected the image artifacts directory}
diagnostics_dir=${4:?Expected the diagnostics directory}

case "$app" in
    catalog-api) apps=(catalog-api) ;;
    frontend) apps=(catalog-api frontend) ;;
    *) printf 'Unknown app: %s\n' "$app" >&2; exit 1 ;;
esac

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cluster_kube=(kubectl --context "kind-$cluster")
namespace=
forward_pid=
mkdir -p "$diagnostics_dir"

stop_port_forward() {
    if [[ -n "$forward_pid" ]]; then
        kill "$forward_pid" 2>/dev/null || true
        wait "$forward_pid" 2>/dev/null || true
        forward_pid=
    fi
}

cleanup() {
    local status=$?
    stop_port_forward
    if [[ -n "$namespace" ]]; then
        if ((status != 0)); then
            printf 'Saving deployment diagnostics to %s\n' "$diagnostics_dir" >&2
            "${kube[@]}" --request-timeout=10s get deployments,replicasets,pods,services,endpointslices -o wide \
                > "$diagnostics_dir/resources.txt" 2>&1 || true
            "${kube[@]}" --request-timeout=10s get events --sort-by=.metadata.creationTimestamp \
                > "$diagnostics_dir/events.txt" 2>&1 || true
            "${kube[@]}" --request-timeout=10s describe pods \
                > "$diagnostics_dir/pods.txt" 2>&1 || true
            "${kube[@]}" --request-timeout=10s logs -l 'app in (catalog-api,frontend)' --prefix --tail=100 \
                > "$diagnostics_dir/containers.log" 2>&1 || true
            "${kube[@]}" --request-timeout=10s logs -l 'app in (catalog-api,frontend)' --prefix --tail=100 --previous \
                > "$diagnostics_dir/previous-containers.log" 2>&1 || true
        fi
        "${cluster_kube[@]}" --request-timeout=10s delete namespace "$namespace" --wait=false \
            --ignore-not-found || true
    fi
}

start_port_forward() {
    local service=$1 log=$2 attempt port

    "${kube[@]}" port-forward --address 127.0.0.1 --pod-running-timeout=30s \
        "service/$service" :8080 > "$log" 2>&1 &
    forward_pid=$!

    for ((attempt = 0; attempt < 30; attempt++)); do
        port=$(sed -nE 's/^Forwarding from 127\.0\.0\.1:([0-9]+) -> 8080$/\1/p' "$log")
        if [[ -n "$port" ]]; then
            base_url="http://127.0.0.1:$port"
            curl --fail --silent --show-error --connect-timeout 2 --max-time 5 \
                "$base_url/healthz" > /dev/null
            return
        fi
        if ! kill -0 "$forward_pid" 2>/dev/null; then
            break
        fi
        sleep 1
    done

    printf 'Could not forward HTTP traffic to %s\n' "$service" >&2
    cat "$log" >&2
    return 1
}

frontend_smoke() {
    bash "$repo_root/apps/frontend/testing/smoke.sh" "$base_url"
    bash "$repo_root/apps/catalog-api/testing/smoke.sh" "$base_url" --via-frontend
}

wait_for_catalog_outage() {
    local attempt status
    for ((attempt = 0; attempt < 10; attempt++)); do
        status=$(curl --silent --show-error --connect-timeout 2 --max-time 5 \
            --output "$diagnostics_dir/catalog-unavailable-response.txt" --write-out '%{http_code}' \
            "$base_url/v1/catalog")
        if [[ "$status" == 502 || "$status" == 503 || "$status" == 504 ]]; then
            return
        fi
        sleep 1
    done
    printf 'Expected an upstream HTTP error while Catalog API was stopped, got %s\n' "$status" >&2
    return 1
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

clusters=$(kind get clusters)
if ! grep -Fxq -- "$cluster" <<< "$clusters"; then
    printf 'kind cluster not found: %s\n' "$cluster" >&2
    exit 1
fi

for service in "${apps[@]}"; do
    archive="$images_dir/image-$service/image.tar"
    if [[ ! -s "$archive" ]]; then
        printf 'Image archive missing or empty: %s\n' "$archive" >&2
        exit 1
    fi
    kind load image-archive "$archive" --name "$cluster"
done

namespace=$("${cluster_kube[@]}" --request-timeout=10s create -f - -o jsonpath='{.metadata.name}' <<'YAML'
apiVersion: v1
kind: Namespace
metadata:
    generateName: kadrroom-testing-
    labels:
        pod-security.kubernetes.io/enforce: restricted
YAML
)
kube=("${cluster_kube[@]}" --namespace "$namespace")

for service in "${apps[@]}"; do
    "${kube[@]}" --request-timeout=15s apply -f "$repo_root/apps/$service/testing/kubernetes.yml"
    "${kube[@]}" rollout status "deployment/$service" --timeout=100s
done

start_port_forward "$app" "$diagnostics_dir/port-forward-initial.log"
if [[ "$app" == frontend ]]; then
    printf 'Checking frontend and catalog through the same URL\n'
    frontend_smoke

    printf 'Stopping Catalog API and checking frontend availability\n'
    "${kube[@]}" --request-timeout=15s scale deployment/catalog-api --replicas=0
    wait_for_catalog_outage
    curl --fail --silent --show-error --max-time 5 "$base_url/healthz" > /dev/null
    bash "$repo_root/apps/frontend/testing/smoke.sh" "$base_url"

    printf 'Restoring Catalog API and checking recovery through the running frontend\n'
    "${kube[@]}" --request-timeout=15s scale deployment/catalog-api --replicas=1
    "${kube[@]}" rollout status deployment/catalog-api --timeout=100s
    frontend_smoke
else
    printf 'Checking Catalog API probes and response contract\n'
    bash "$repo_root/apps/catalog-api/testing/smoke.sh" "$base_url"
fi

printf 'Restarting %s and checking the replacement pod\n' "$app"
stop_port_forward
"${kube[@]}" --request-timeout=15s rollout restart "deployment/$app"
"${kube[@]}" rollout status "deployment/$app" --timeout=100s
start_port_forward "$app" "$diagnostics_dir/port-forward-restarted.log"
if [[ "$app" == frontend ]]; then
    frontend_smoke
else
    bash "$repo_root/apps/catalog-api/testing/smoke.sh" "$base_url"
fi
