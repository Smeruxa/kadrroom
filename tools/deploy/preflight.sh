#!/usr/bin/env bash
set +x
set -euo pipefail
export LC_ALL=C

fail() {
    printf '%s\n' "$*" >&2
    exit 1
}

if [[ $# != 5 || ! $1 =~ ^(test|production)$ || ! $2 =~ ^(bootstrap|[0-9a-f]{40})$ || $3 != /* ]]; then
    fail 'Usage: bash tools/deploy/preflight.sh <test|production> <bootstrap|commit-sha> <absolute-kubeconfig> <public-hostname> <admin-hostname>'
fi

namespace="kadrroom-$1"
peer=kadrroom-production
if [[ $namespace == "$peer" ]]; then
    peer=kadrroom-test
fi
revision=$2
kube=(kubectl --kubeconfig "$3" --request-timeout=15s)
public_host=$4
admin_host=$5

label='[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?'
for host in "$public_host" "$admin_host"; do
    [[ ${#host} -le 253 && $host =~ ^($label\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$ && $host != *.invalid ]] ||
        fail 'Expected lowercase DNS hostnames.'
done
[[ $public_host != "$admin_host" ]] || fail 'Public and admin hostnames must differ.'
[[ $("${kube[@]}" config view --minify -o 'jsonpath={.clusters[0].cluster.server}') == https://127.0.0.1:6443 ]] ||
    fail 'Run the checks on the VPS with a kubeconfig for the local K3s API.'

"${kube[@]}" get nodes -o json | jq -e '
    .items | length == 1 and all(.[];
        .status.nodeInfo.architecture == "amd64"
        and (.status.nodeInfo.kubeletVersion | contains("k3s"))
        and (.spec.podCIDR | type == "string" and length > 0)
        and any(.status.conditions[]; .type == "Ready" and .status == "True")
    )
' > /dev/null || fail 'Expected one Ready amd64 K3s node with a Pod CIDR.'
"${kube[@]}" -n kube-system get deployment traefik coredns -o json | jq -e '
    .items | length == 2 and all(.[]; (.status.availableReplicas // 0) > 0)
' > /dev/null || fail 'Traefik or CoreDNS is unavailable.'
"${kube[@]}" get ingressclass/traefik storageclass/local-path -o name > /dev/null ||
    fail 'Expected the Traefik IngressClass and the local-path StorageClass.'
"${kube[@]}" get namespace "$namespace" "$peer" -o json | jq -e '
    .items | length == 2 and all(.[];
        .status.phase == "Active" and .metadata.labels["pod-security.kubernetes.io/enforce"] == "restricted"
    )
' > /dev/null || fail 'Expected two active restricted KadrRoom namespaces.'

for ns in "$namespace" "$peer"; do
    "${kube[@]}" -n "$ns" get ingress frontend admin-frontend -o json |
        jq -e --arg public "$public_host" --arg admin "$admin_host" --arg selected "$namespace" '
            .items | length == 2 and all(.[];
                .spec.ingressClassName == "traefik"
                and .metadata.annotations["traefik.ingress.kubernetes.io/router.entrypoints"] == "websecure"
                and .metadata.annotations["traefik.ingress.kubernetes.io/router.tls"] == "true"
                and .spec.defaultBackend == null
                and (.spec.rules | length == 1)
                and (.spec.tls | length == 1)
                and (.spec.rules[0].host | type == "string" and length > 0)
                and .spec.tls[0].secretName == (.metadata.name + "-tls")
                and .spec.tls[0].hosts == [.spec.rules[0].host]
                and .spec.rules[0].http.paths == [{
                    path: "/", pathType: "Prefix", backend: {service: {name: .metadata.name, port: {name: "http"}}}
                }]
                and (if .metadata.namespace == $selected
                     then .spec.rules[0].host == (if .metadata.name == "frontend" then $public else $admin end)
                     else .spec.rules[0].host != $public and .spec.rules[0].host != $admin
                     end)
            ) and (map(.spec.rules[0].host) | unique | length == 2)
        ' > /dev/null || fail "Ingress, TLS references, or hostname isolation failed in $ns."
done
"${kube[@]}" -n "$namespace" get configmap public-origin admin-origin -o json |
    jq -e --arg public "$public_host" --arg admin "$admin_host" '
        .items | map({key: .metadata.name, value: .data.url}) | from_entries
        | . == {"public-origin": ("https://" + $public), "admin-origin": ("https://" + $admin)}
    ' > /dev/null || fail "Public and admin origins do not match the Ingress in $namespace."

subject="system:serviceaccount:$namespace:kadrroom-deployer"
deployer=("${kube[@]}" --as "$subject" --as-group system:authenticated
    --as-group system:serviceaccounts --as-group "system:serviceaccounts:$namespace")

check_access() {
    local expected=$1 scope=$2 verb=$3 resource=$4 decision
    decision=$("${deployer[@]}" -n "$scope" auth can-i "$verb" "$resource") || true
    [[ $decision == "$expected" ]] || fail "Expected '$expected' for deploy account: $scope $verb $resource."
}

apps=(identity-api admin-api catalog-api rooms-api media-worker frontend admin-frontend)
for app in "${apps[@]}"; do
    for verb in get list watch patch; do
        check_access yes "$namespace" "$verb" "deployment/$app"
    done
    check_access no "$peer" get "deployment/$app"
    check_access no "$peer" patch "deployment/$app"
done
for verb in get list watch; do
    check_access no "$namespace" "$verb" secrets
done
for secret in identity-config auth-proxy-config rooms-config catalog-config admin-config media-worker-config \
    catalog-rooms-contract postgres-bootstrap livekit-config frontend-tls admin-frontend-tls registry-pull \
    kadrroom-deployer-token; do
    check_access no "$namespace" get "secret/$secret"
    check_access no "$namespace" patch "secret/$secret"
done
check_access no "$namespace" patch deployment/not-a-release-workload

"${kube[@]}" -n "$namespace" get deployment "${apps[@]}" -o json |
    jq -e --arg revision "$revision" --arg registry 'ghcr.io/smeruxa/kadrroom' '
        .items | length == 7 and all(.[];
            (.metadata.name) as $app
            | ($registry + "/" + $app + "@sha256:") as $prefix
            | ([.spec.template.spec.containers[] | select(.name == $app).image]) as $images
            | ("0" * 64) as $zero
            | .metadata.annotations["kadrroom.io/revision"] == $revision
            and ($images | length == 1)
            and (if $revision == "bootstrap"
                 then .spec.replicas == 0 and (.status.replicas // 0) == 0 and $images[0] == ($prefix + $zero)
                 else .spec.replicas == 1 and .status.replicas == 1 and .status.readyReplicas == 1
                      and .status.availableReplicas == 1 and .status.updatedReplicas == 1
                      and .status.observedGeneration == .metadata.generation
                      and ($images[0] | startswith($prefix) and
                           (.[($prefix | length):] | test("^[0-9a-f]{64}$") and . != $zero))
                 end)
        )
    ' > /dev/null || fail "Expected all seven Deployments in $namespace at revision $revision."

if [[ $revision != bootstrap ]]; then
    for host in "$public_host" "$admin_host"; do
        status=$(curl --disable --noproxy '*' --proto '=https' --silent --show-error --connect-timeout 5 \
            --max-time 12 --output /dev/null --write-out '%{http_code}' "https://$host/") ||
            fail "Could not establish trusted HTTPS to $host from the VPS."
        [[ $status == 200 ]] || fail "Expected HTTP 200 from $host, got $status."
    done
fi

printf 'K3s preflight passed for %s at %s.\n' "$namespace" "$revision"
