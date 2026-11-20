#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
umask 077

fail() {
    printf '%s\n' "$*" >&2
    exit 1
}

if [[ $# != 3 || ! $1 =~ ^(test|production)$ || $2 != /* || $3 != /* ]]; then
    fail 'Expected an environment and absolute paths to its config and state directories.'
fi

environment=$1
config_dir=$2
state_dir=$3
request=${SSH_ORIGINAL_COMMAND:-}
commit_pattern='[0-9a-f]{40}'
digest_pattern='sha256:[0-9a-f]{64}'
command_pattern="^deploy $commit_pattern( $digest_pattern){7}$"
[[ $request =~ $command_pattern ]] || fail 'Expected deploy, a commit SHA, and seven sha256 image digests.'
read -r -a release <<< "$request"
commit=${release[1]}

apps=(identity-api admin-api catalog-api rooms-api media-worker frontend admin-frontend)
digests=("${release[4]}" "${release[7]}" "${release[3]}" "${release[5]}" "${release[8]}" "${release[2]}" "${release[6]}")
registry=ghcr.io/smeruxa/kadrroom
printf -v bootstrap_digest 'sha256:%064d' 0
for digest in "${digests[@]}"; do
    [[ $digest != "$bootstrap_digest" ]] || fail 'The bootstrap image cannot be deployed.'
done

read_hostname() {
    local file=$1 host
    local label='[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?'
    local pattern="^($label\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$"
    IFS= read -r host < "$file" || fail "Cannot read hostname from $file."
    [[ ${#host} -le 253 && $host =~ $pattern && $host != *.invalid ]] || fail "Invalid hostname in $file."
    printf '%s\n' "$host"
}

public_host=$(read_hostname "$config_dir/host")
admin_host=$(read_hostname "$config_dir/admin-host")
[[ $public_host != "$admin_host" ]] || fail 'Public and admin hostnames must be different.'
origin="https://$public_host"
admin_origin="https://$admin_host"
kube=(kubectl --kubeconfig "$config_dir/kubeconfig" --namespace "kadrroom-$environment")
pending="$state_dir/pending"

exec 9> "$state_dir/lock"
flock -n 9 || fail "Another release is running for $environment."

patch_deployment() {
    local app=$1 revision=$2 replicas=$3 image=$4 patch
    patch=$(jq -cn --arg app "$app" --arg revision "$revision" --arg image "$image" \
        --argjson replicas "$replicas" '
        {
            metadata: {annotations: {"kadrroom.io/revision": $revision}},
            spec: {replicas: $replicas, template: {spec: {containers: [{name: $app, image: $image}]}}}
        }') || return 1
    "${kube[@]}" --request-timeout=15s patch "deployment/$app" --type=strategic --patch "$patch"
}

wait_for_deployment() {
    "${kube[@]}" rollout status "deployment/$1" --timeout=120s
}

check_routes() {
    local -a https=(curl --disable --noproxy '*' --proto '=https' --silent --show-error --connect-timeout 5 --max-time 8)
    local site response status
    for site in "$origin" "$admin_origin"; do
        status=$("${https[@]}" --output /dev/null --write-out '%{http_code}' "$site/") || return 1
        [[ $status == 200 ]] || return 1
        response=$("${https[@]}" --write-out '\n%{http_code}' "$site/v1/auth/session") || return 1
        [[ ${response##*$'\n'} == 401 ]] &&
            jq -e '.code == "unauthenticated"' <<< "${response%$'\n'*}" > /dev/null || return 1
    done
    "${https[@]}" --fail "$origin/v1/catalog" | jq -e '.items | type == "array"' > /dev/null || return 1

    status=$("${https[@]}" --output /dev/null --write-out '%{http_code}' "$origin/v1/admin/catalog") || return 1
    [[ $status == 404 ]] || return 1
    response=$("${https[@]}" --write-out '\n%{http_code}' "$admin_origin/v1/admin/catalog") || return 1
    [[ ${response##*$'\n'} == 401 ]] &&
        jq -e '.code == "unauthenticated"' <<< "${response%$'\n'*}" > /dev/null || return 1

    response=$("${https[@]}" --http1.1 --write-out '\n%{http_code}' \
        --header "Origin: $origin" --header 'Connection: Upgrade' --header 'Upgrade: websocket' \
        --header 'Sec-WebSocket-Version: 13' --header 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
        "$origin/v1/rooms/smoke/ws") || return 1
    [[ ${response##*$'\n'} == 403 ]] &&
        jq -e '.code == "invalid_ticket"' <<< "${response%$'\n'*}" > /dev/null
}

wait_for_routes() {
    local delay
    for delay in 0 2 4 8 8; do
        sleep "$delay"
        if check_routes; then
            return 0
        fi
    done
    printf '%s\n' 'HTTPS check failed for the public site, admin site, or one of their API routes.' >&2
    return 1
}

valid_previous() {
    local app=$1 revision=$2 replicas=$3 image=$4
    local image_pattern="^${registry//./\\.}/$app@$digest_pattern$"
    if [[ $revision == bootstrap ]]; then
        [[ $replicas == 0 && $image == "$registry/$app@$bootstrap_digest" ]]
    else
        [[ $revision =~ ^$commit_pattern$ && $replicas == 1 && $image =~ $image_pattern &&
            $image != "$registry/$app@$bootstrap_digest" ]]
    fi
}

restore_previous() {
    local revision replicas image app
    local previous_revision='' restored=true
    local -a revisions=() counts=() images=()
    local i=0
    while IFS=$'\t' read -r revision replicas image; do
        if (( i >= ${#apps[@]} )) || ! valid_previous "${apps[i]}" "$revision" "$replicas" "$image" ||
            [[ -n $previous_revision && $revision != "$previous_revision" ]]; then
            printf '%s\n' 'Release journal is invalid. Review it before retrying.' >&2
            return 1
        fi
        previous_revision=$revision
        revisions+=("$revision")
        counts+=("$replicas")
        images+=("$image")
        i=$((i + 1))
    done < "$pending"
    if (( i != ${#apps[@]} )); then
        printf '%s\n' 'Release journal is incomplete. Review it before retrying.' >&2
        return 1
    fi

    printf 'Restoring %s in %s.\n' "$previous_revision" "$environment" >&2
    for i in "${!apps[@]}"; do
        patch_deployment "${apps[i]}" "${revisions[i]}" "${counts[i]}" "${images[i]}" || restored=false
    done
    for app in "${apps[@]}"; do
        wait_for_deployment "$app" || restored=false
    done
    [[ $restored == true ]] || return 1
    if [[ $previous_revision != bootstrap ]]; then
        wait_for_routes || return 1
    fi
    rm -- "$pending"
}

if [[ -f $pending ]]; then
    restore_previous || fail 'Previous rollback is incomplete. Journal retained.'
fi

snapshot=()
previous_revision=
for app in "${apps[@]}"; do
    record=$("${kube[@]}" --request-timeout=15s get "deployment/$app" --output=json |
        jq -er --arg app "$app" '
            [ .metadata.annotations["kadrroom.io/revision"], .spec.replicas,
              ([.spec.template.spec.containers[] | select(.name == $app).image] | select(length == 1) | .[0])
            ] | @tsv
        ') || fail "Cannot read the current revision of $app."
    IFS=$'\t' read -r revision replicas image <<< "$record"
    valid_previous "$app" "$revision" "$replicas" "$image" ||
        fail "$app must be bootstrapped or reference an immutable release."
    [[ -z $previous_revision || $previous_revision == "$revision" ]] ||
        fail 'Deployments report mixed revisions. Restore a consistent release before retrying.'
    previous_revision=$revision
    snapshot+=("$record")
done

journal_tmp=
finish() {
    local status=$?
    trap - EXIT
    trap '' INT TERM HUP
    if [[ -n $journal_tmp ]]; then
        rm -f -- "$journal_tmp" || true
    fi
    if [[ -f $pending ]]; then
        if restore_previous; then
            printf '%s\n' 'Rollback complete.' >&2
        else
            printf '%s\n' 'Rollback incomplete. Journal retained for retry.' >&2
        fi
        exit 1
    fi
    exit "$status"
}
trap finish EXIT
trap 'exit 1' INT TERM HUP

journal_tmp=$(mktemp "$state_dir/pending.XXXXXXXX")
printf '%s\n' "${snapshot[@]}" > "$journal_tmp"
mv -- "$journal_tmp" "$pending"
journal_tmp=

for i in "${!apps[@]}"; do
    app=${apps[i]}
    patch_deployment "$app" "$commit" 1 "$registry/$app@${digests[i]}"
    wait_for_deployment "$app"
done
wait_for_routes
rm -- "$pending"
printf 'Deployed %s to %s.\n' "$commit" "$environment"
