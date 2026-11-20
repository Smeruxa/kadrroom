#!/usr/bin/env bash
set -euo pipefail

release_script=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/release.sh
FIXTURE=$(mktemp -d)
export FIXTURE
trap 'rm -rf -- "$FIXTURE"' EXIT
mkdir "$FIXTURE/bin" "$FIXTURE/config" "$FIXTURE/state"
printf '%s\n' 'test.example.org' > "$FIXTURE/config/host"
printf '%s\n' 'admin.example.org' > "$FIXTURE/config/admin-host"

apps=(frontend catalog-api identity-api rooms-api admin-frontend admin-api media-worker)

cat > "$FIXTURE/bin/kubectl" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
[[ "$1 $2 $3 $4" == "--kubeconfig $FIXTURE/config/kubeconfig --namespace kadrroom-test" ]]
shift 4
if [[ $1 == --request-timeout=* ]]; then
    shift
fi

operation=$1
if [[ $operation == rollout ]]; then
    [[ $2 == status ]]
    shift
fi
app=${2#deployment/}
printf '%s %s\n' "$operation" "$app" >> "$FIXTURE/kubectl.log"
case "$operation" in
    get)
        jq -e --arg app "$app" '.[$app]' "$FIXTURE/deployments"
        ;;
    patch)
        [[ $3 == --type=strategic && $4 == --patch ]]
        jq -e --arg app "$app" --argjson change "$5" '.[$app] *= $change' \
            "$FIXTURE/deployments" > "$FIXTURE/staged"
        mv "$FIXTURE/staged" "$FIXTURE/deployments"

        if [[ $app == rooms-api && -f $FIXTURE/interrupt ]]; then
            rm "$FIXTURE/interrupt"
            kill -KILL "$PPID"
        elif [[ $app == identity-api && -f $FIXTURE/fail-patch ]]; then
            rm "$FIXTURE/fail-patch"
            exit 1
        fi
        ;;
    rollout)
        [[ $3 == --timeout=* ]]
        if [[ $app == rooms-api && -f $FIXTURE/fail-rollout ]] &&
            jq -e --arg app "$app" --arg revision "$(cat "$FIXTURE/fail-rollout")" \
                '.[$app].metadata.annotations["kadrroom.io/revision"] == $revision' "$FIXTURE/deployments" > /dev/null; then
            exit 1
        fi
        ;;
    *) exit 1 ;;
esac
SCRIPT

cat > "$FIXTURE/bin/curl" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
[[ $1 == --disable ]]
case "${!#}" in
    https://test.example.org/ | https://admin.example.org/) printf '200' ;;
    https://test.example.org/v1/catalog) printf '%s\n' '{"items":[]}' ;;
    https://test.example.org/v1/auth/session | https://admin.example.org/v1/auth/session)
        printf '%s\n' '{"code":"unauthenticated"}' 401
        ;;
    https://test.example.org/v1/admin/catalog) printf '404' ;;
    https://admin.example.org/v1/admin/catalog)
        if [[ -f $FIXTURE/fail-admin-smoke ]] && jq -e --arg revision "$(cat "$FIXTURE/fail-admin-smoke")" \
            '.["admin-frontend"].metadata.annotations["kadrroom.io/revision"] == $revision' "$FIXTURE/deployments" > /dev/null; then
            printf '%s\n' '{"code":"unavailable"}' 502
        else
            printf '%s\n' '{"code":"unauthenticated"}' 401
        fi
        ;;
    https://test.example.org/v1/rooms/smoke/ws)
        [[ " $* " == *' --http1.1 '* && " $* " == *' Origin: https://test.example.org '* &&
            " $* " == *' Upgrade: websocket '* ]]
        if [[ -f $FIXTURE/fail-smoke ]] && jq -e --arg revision "$(cat "$FIXTURE/fail-smoke")" \
            '.frontend.metadata.annotations["kadrroom.io/revision"] == $revision' "$FIXTURE/deployments" > /dev/null; then
            printf '%s\n' '{"code":"invalid_ticket"}' 502
        else
            printf '%s\n' '{"code":"invalid_ticket"}' 403
        fi
        ;;
    *) exit 1 ;;
esac
SCRIPT

for command in flock sleep; do
    printf '#!/usr/bin/env bash\nexit 0\n' > "$FIXTURE/bin/$command"
done
chmod +x "$FIXTURE/bin/"*
export PATH="$FIXTURE/bin:$PATH"

fail() {
    printf 'Release test failed (%s).\n' "$*" >&2
    if [[ -f $FIXTURE/output ]]; then
        cat "$FIXTURE/output" >&2
    fi
    exit 1
}

write_deployments() {
    local revision=$1 replicas=$2 base=$3
    local records="$FIXTURE/records"
    local app image
    local index=0
    : > "$records"
    for app in "${apps[@]}"; do
        printf -v image 'ghcr.io/smeruxa/kadrroom/%s@sha256:%064d' "$app" "$((base + index * replicas))"
        jq -cn --arg app "$app" --arg revision "$revision" --arg image "$image" --argjson replicas "$replicas" '
            {($app): {
                metadata: {annotations: {"kadrroom.io/revision": $revision}},
                spec: {replicas: $replicas, template: {spec: {containers: [{name: $app, image: $image}]}}}
            }}' >> "$records"
        index=$((index + 1))
    done
    jq -s 'add' "$records"
}

request() {
    local revision=$1 base=$2
    local index
    printf 'deploy %s' "$revision"
    for index in "${!apps[@]}"; do
        printf ' sha256:%064d' "$((base + index))"
    done
}

deploy() {
    SSH_ORIGINAL_COMMAND=$1 bash "$release_script" test "$FIXTURE/config" "$FIXTURE/state" > "$FIXTURE/output" 2>&1
}

assert_deployments() {
    write_deployments "$1" "$2" "$3" > "$FIXTURE/expected"
    jq -S . "$FIXTURE/deployments" > "$FIXTURE/actual"
    jq -S . "$FIXTURE/expected" > "$FIXTURE/expected-sorted"
    cmp "$FIXTURE/expected-sorted" "$FIXTURE/actual" || fail "expected revision $1"
}

printf -v first '%040d' 123
printf -v second '%040d' 456
write_deployments bootstrap 0 0 > "$FIXTURE/deployments"

if deploy "$(request "$first" 10); id"; then
    fail 'unsafe SSH request accepted'
fi
if deploy "$(request "$first" 0)"; then
    fail 'bootstrap digest accepted'
fi
cp "$FIXTURE/config/host" "$FIXTURE/config/admin-host"
if deploy "$(request "$first" 10)"; then
    fail 'shared public and admin hostname accepted'
fi
printf '%s\n' 'admin.example.org' > "$FIXTURE/config/admin-host"
[[ ! -e $FIXTURE/kubectl.log ]] || fail 'validation reached Kubernetes'

touch "$FIXTURE/fail-patch"
if deploy "$(request "$first" 10)"; then
    fail 'failed bootstrap patch reported success'
fi
assert_deployments bootstrap 0 0
[[ ! -e $FIXTURE/state/pending ]] || fail 'bootstrap rollback left its journal'

deploy "$(request "$first" 10)" || fail 'first release'
assert_deployments "$first" 1 10
[[ ! -e $FIXTURE/state/pending ]] || fail 'first release left its journal'

printf '%s\n' "$second" > "$FIXTURE/fail-rollout"
if deploy "$(request "$second" 20)"; then
    fail 'failed readiness reported success'
fi
assert_deployments "$first" 1 10
[[ ! -e $FIXTURE/state/pending ]] || fail 'readiness rollback left its journal'
rm "$FIXTURE/fail-rollout"

printf '%s\n' "$second" > "$FIXTURE/fail-smoke"
if deploy "$(request "$second" 20)"; then
    fail 'failed HTTPS smoke check reported success'
fi
assert_deployments "$first" 1 10
[[ ! -e $FIXTURE/state/pending ]] || fail 'HTTPS rollback left its journal'
rm "$FIXTURE/fail-smoke"

printf '%s\n' "$second" > "$FIXTURE/fail-admin-smoke"
if deploy "$(request "$second" 20)"; then
    fail 'failed admin HTTPS check reported success'
fi
assert_deployments "$first" 1 10
[[ ! -e $FIXTURE/state/pending ]] || fail 'admin HTTPS rollback left its journal'
rm "$FIXTURE/fail-admin-smoke"

touch "$FIXTURE/interrupt"
if deploy "$(request "$second" 20)" 2> "$FIXTURE/interruption"; then
    fail 'interrupted release reported success'
fi
[[ -s $FIXTURE/state/pending ]] || fail 'interruption lost its journal'
jq -e --arg first "$first" --arg second "$second" '
    .frontend.metadata.annotations["kadrroom.io/revision"] == $first and
    .["rooms-api"].metadata.annotations["kadrroom.io/revision"] == $second
' "$FIXTURE/deployments" > /dev/null || fail 'interruption did not leave a mixed release'

deploy "$(request "$second" 20)" || fail 'resume after interruption'
assert_deployments "$second" 1 20
[[ ! -e $FIXTURE/state/pending ]] || fail 'recovery left its journal'
printf '%s\n' 'Release validation, rollback and interrupted recovery passed.'
