set -euo pipefail

cluster=${1:?Expected the kind cluster name}
images_dir=${2:?Expected the image artifacts directory}
diagnostics_dir=${3:?Expected the diagnostics directory}
shift 3
if (($# != 0)); then
    if [[ $1 != -- || $# == 1 ]]; then
        printf 'Expected -- followed by the test command.\n' >&2
        exit 2
    fi
    shift
fi

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
apps=(catalog-api identity-api rooms-api frontend admin-api admin-frontend media-worker)
cluster_kube=(kubectl --context "kind-$cluster")
namespace=
forward_pids=()
mkdir -p "$diagnostics_dir"
manifest_dir=$(mktemp -d)

web_port=${KADRROOM_WEB_PORT:-4173}
admin_port=${KADRROOM_ADMIN_WEB_PORT:-4183}
mail_port=${KADRROOM_MAIL_PORT:-8025}
rtc_port=${KADRROOM_RTC_PORT:-7880}
rtc_tcp_port=${KADRROOM_RTC_TCP_PORT:-7881}

cleanup() {
    local status=$? pid
    for pid in "${forward_pids[@]-}"; do
        if [[ -n "$pid" ]]; then
            kill "$pid" 2>/dev/null || true
            wait "$pid" 2>/dev/null || true
        fi
    done
    if [[ -n "$namespace" ]]; then
        if ((status != 0)); then
            "${kube[@]}" --request-timeout=10s get deployments,statefulsets,pods,services,endpointslices,pvc -o wide \
                > "$diagnostics_dir/resources.txt" 2>&1 || true
            "${kube[@]}" --request-timeout=10s get events --sort-by=.metadata.creationTimestamp \
                > "$diagnostics_dir/events.txt" 2>&1 || true
            "${kube[@]}" --request-timeout=10s describe pods \
                > "$diagnostics_dir/pods.txt" 2>&1 || true
            "${kube[@]}" --request-timeout=10s logs -l app --all-containers --prefix --tail=100 \
                --max-log-requests=12 > "$diagnostics_dir/containers.log" 2>&1 || true
            "${kube[@]}" --request-timeout=10s logs -l app --all-containers --prefix --tail=100 \
                --max-log-requests=12 --previous > "$diagnostics_dir/previous-containers.log" 2>&1 || true
        fi
        "${cluster_kube[@]}" --request-timeout=10s delete namespace "$namespace" --wait=false \
            --ignore-not-found || true
    fi
    rm -rf "$manifest_dir"
}

create_stack_config() {
    local bootstrap_password identity_password rooms_password admin_password reader_password worker_password
    local internal_token signing_key mail_key auth_proxy_token livekit_key livekit_secret
    bootstrap_password=$(openssl rand -hex 32)
    identity_password=$(openssl rand -hex 32)
    rooms_password=$(openssl rand -hex 32)
    admin_password=$(openssl rand -hex 32)
    reader_password=$(openssl rand -hex 32)
    worker_password=$(openssl rand -hex 32)
    internal_token=$(openssl rand -hex 32)
    signing_key=$(openssl rand -hex 32)
    mail_key=$(openssl rand -hex 32)
    auth_proxy_token=$(openssl rand -hex 32)
    livekit_key=kadrroom-ci
    livekit_secret=$(openssl rand -hex 32)

    "${kube[@]}" create serviceaccount kadrroom-runtime
    "${kube[@]}" create configmap public-origin --from-literal="url=$KADRROOM_E2E_BASE_URL"
    "${kube[@]}" create configmap admin-origin --from-literal="url=$KADRROOM_E2E_ADMIN_URL"
    "${kube[@]}" create configmap postgres-init \
        --from-file="init-databases.sh=$repo_root/deploy/k8s/postgres/base/init-databases.sh"
    "${kube[@]}" create configmap livekit-server --from-literal="config=port: 7880
room:
    auto_create: false
rtc:
    tcp_port: $rtc_tcp_port
    use_external_ip: false"
    "${kube[@]}" create secret generic postgres-bootstrap \
        --from-literal="POSTGRES_PASSWORD=$bootstrap_password" \
        --from-literal="KADRROOM_IDENTITY_DB_PASSWORD=$identity_password" \
        --from-literal="KADRROOM_ROOMS_DB_PASSWORD=$rooms_password" \
        --from-literal="KADRROOM_CATALOG_DB_PASSWORD=$admin_password" \
        --from-literal="KADRROOM_CATALOG_READ_DB_PASSWORD=$reader_password" \
        --from-literal="KADRROOM_CATALOG_WORKER_DB_PASSWORD=$worker_password"
    "${kube[@]}" create secret generic auth-proxy-config \
        --from-literal="KADRROOM_AUTH_PROXY_TOKEN=$auth_proxy_token" \
        --from-literal='KADRROOM_TRUSTED_PROXY_CIDR=unix:'
    "${kube[@]}" create secret generic identity-config \
        --from-literal="KADRROOM_IDENTITY_DATABASE_URL=postgres://identity:$identity_password@postgres:5432/identity?sslmode=disable" \
        --from-literal="KADRROOM_MAIL_TOKEN_KEY=$mail_key" \
        --from-literal="KADRROOM_OPERATOR_EMAIL=$KADRROOM_E2E_OPERATOR_EMAIL"
    "${kube[@]}" create secret generic rooms-config \
        --from-literal="KADRROOM_ROOMS_DATABASE_URL=postgres://rooms:$rooms_password@postgres:5432/rooms?sslmode=disable"
    "${kube[@]}" create secret generic admin-config \
        --from-literal="KADRROOM_ADMIN_DATABASE_URL=postgres://catalog:$admin_password@postgres:5432/catalog?sslmode=disable"
    "${kube[@]}" create secret generic catalog-config \
        --from-literal="KADRROOM_CATALOG_DATABASE_URL=postgres://catalog_read:$reader_password@postgres:5432/catalog?sslmode=disable" \
        --from-literal="KADRROOM_CATALOG_SIGNING_KEY=$signing_key"
    "${kube[@]}" create secret generic media-worker-config \
        --from-literal="KADRROOM_CATALOG_DATABASE_URL=postgres://catalog_worker:$worker_password@postgres:5432/catalog?sslmode=disable"
    "${kube[@]}" create secret generic catalog-rooms-contract \
        --from-literal="KADRROOM_CATALOG_INTERNAL_TOKEN=$internal_token"
    "${kube[@]}" create secret generic livekit-config \
        --from-literal="LIVEKIT_KEYS=$livekit_key: $livekit_secret" \
        --from-literal="KADRROOM_LIVEKIT_URL=ws://127.0.0.1:$rtc_port" \
        --from-literal='KADRROOM_LIVEKIT_INTERNAL_URL=http://livekit:7880' \
        --from-literal="KADRROOM_LIVEKIT_API_KEY=$livekit_key" \
        --from-literal="KADRROOM_LIVEKIT_API_SECRET=$livekit_secret"
}

prepare_manifests() {
    local app
    cp "$repo_root/deploy/k8s/postgres/base/postgres.yaml" \
        "$repo_root/deploy/k8s/overlays/test/mailpit.yaml" \
        "$repo_root/deploy/k8s/overlays/test/smtp.yaml" \
        "$repo_root/tools/ci/livekit-testing.yml" "$manifest_dir/"
    for app in "${apps[@]}"; do
        cp "$repo_root/deploy/k8s/base/$app.yaml" "$manifest_dir/"
    done
    {
        cat <<'YAML'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
    - postgres.yaml
    - mailpit.yaml
    - livekit-testing.yml
YAML
        printf '    - %s.yaml\n' "${apps[@]}"
        cat <<'YAML'
patches:
    - path: smtp.yaml
    - target:
          kind: StatefulSet
          name: postgres
      patch: |-
          - op: remove
            path: /spec/volumeClaimTemplates/0/spec/storageClassName
    - target:
          kind: PersistentVolumeClaim
          name: catalog-media
      patch: |-
          - op: remove
            path: /spec/storageClassName
YAML
        for app in "${apps[@]}"; do
            cat <<YAML
    - target:
          kind: Deployment
          name: $app
      patch: |-
          - op: replace
            path: /spec/template/spec/containers/0/image
            value: kadrroom/$app:ci
          - op: add
            path: /spec/template/spec/containers/0/imagePullPolicy
            value: Never
          - op: add
            path: /spec/replicas
            value: 0
YAML
        done
        for app in identity-api rooms-api; do
            cat <<YAML
    - patch: |-
          apiVersion: apps/v1
          kind: Deployment
          metadata:
              name: $app
          spec:
              template:
                  spec:
                      containers:
                          - name: $app
                            env:
                                - name: KADRROOM_SECURE_COOKIES
                                  value: 'false'
YAML
        done
    } > "$manifest_dir/kustomization.yaml"
}

start_forward() {
    local resource=$1 label=$2 attempt mapping port ready pid
    shift 2
    local log="$diagnostics_dir/port-forward-$label.log"
    "${kube[@]}" port-forward --address 127.0.0.1 --pod-running-timeout=30s \
        "$resource" "$@" > "$log" 2>&1 &
    pid=$!
    forward_pids+=("$pid")

    for ((attempt = 0; attempt < 30; attempt++)); do
        ready=true
        for mapping in "$@"; do
            port=${mapping%%:*}
            if ! grep -Fq "Forwarding from 127.0.0.1:$port -> " "$log"; then
                ready=false
            fi
        done
        if [[ "$ready" == true ]]; then
            return
        fi
        if ! kill -0 "$pid" 2>/dev/null; then
            break
        fi
        sleep 1
    done
    printf 'Could not forward ports to %s.\n' "$resource" >&2
    cat "$log" >&2
    return 1
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

seen_ports=:
for port in "$web_port" "$admin_port" "$mail_port" "$rtc_port" "$rtc_tcp_port"; do
    if [[ ! $port =~ ^[1-9][0-9]{3,4}$ ]] || ((port < 1024 || port > 65535)); then
        printf 'Expected a TCP port from 1024 to 65535, got %s.\n' "$port" >&2
        exit 2
    fi
    if [[ $seen_ports == *":$port:"* ]]; then
        printf 'Each test endpoint must use a different local port; got %s twice.\n' "$port" >&2
        exit 2
    fi
    seen_ports+="$port:"
    if command -v nc >/dev/null && nc -z -w 1 127.0.0.1 "$port" >/dev/null 2>&1; then
        printf 'Local TCP port %s is already in use.\n' "$port" >&2
        exit 2
    fi
done

clusters=$(kind get clusters)
if ! grep -Fxq -- "$cluster" <<< "$clusters"; then
    printf 'kind cluster not found: %s\n' "$cluster" >&2
    exit 1
fi

for app in "${apps[@]}"; do
    archive="$images_dir/image-$app/image.tar"
    if [[ ! -s "$archive" ]]; then
        printf 'Image archive missing or empty: %s\n' "$archive" >&2
        exit 1
    fi
done

for app in "${apps[@]}"; do
    kind load image-archive "$images_dir/image-$app/image.tar" --name "$cluster"
done

namespace=$("${cluster_kube[@]}" --request-timeout=10s create -f - -o jsonpath='{.metadata.name}' <<'YAML'
apiVersion: v1
kind: Namespace
metadata:
    generateName: kadrroom-stack-testing-
    labels:
        pod-security.kubernetes.io/enforce: restricted
YAML
)
kube=("${cluster_kube[@]}" --namespace "$namespace")

export KADRROOM_E2E_BASE_URL="http://127.0.0.1:$web_port"
export KADRROOM_E2E_ADMIN_URL="http://127.0.0.1:$admin_port"
export KADRROOM_E2E_MAIL_URL="http://127.0.0.1:$mail_port"
export KADRROOM_E2E_OPERATOR_EMAIL=${KADRROOM_E2E_OPERATOR_EMAIL:-operator@example.test}

prepare_manifests
create_stack_config
"${kube[@]}" --request-timeout=20s apply -k "$manifest_dir"
"${kube[@]}" rollout status statefulset/postgres --timeout=180s
"${kube[@]}" scale deployment/admin-api deployment/identity-api --replicas=1
"${kube[@]}" rollout status deployment/admin-api --timeout=120s
"${kube[@]}" scale deployment/catalog-api deployment/rooms-api deployment/media-worker \
    deployment/frontend deployment/admin-frontend --replicas=1
for app in "${apps[@]}" mailpit livekit; do
    "${kube[@]}" rollout status "deployment/$app" --timeout=120s
done

start_forward service/frontend frontend "$web_port:8080"
start_forward service/admin-frontend admin-frontend "$admin_port:8080"
start_forward service/mailpit mailpit "$mail_port:8025"
start_forward deployment/livekit livekit "$rtc_port:7880" "$rtc_tcp_port:$rtc_tcp_port"

bash "$repo_root/tools/ci/wait-testing-stack.sh" "$KADRROOM_E2E_BASE_URL" "$KADRROOM_E2E_MAIL_URL" "$KADRROOM_E2E_ADMIN_URL"
curl --fail --silent --show-error --connect-timeout 2 --max-time 5 "http://127.0.0.1:$rtc_port/" > /dev/null
export KADRROOM_E2E_RTC=${KADRROOM_E2E_RTC:-1}
if (($# != 0)); then
    "$@"
fi
