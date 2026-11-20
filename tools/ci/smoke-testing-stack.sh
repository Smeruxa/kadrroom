set +x
set -euo pipefail
umask 077

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
web_url=${KADRROOM_E2E_BASE_URL:?Expected the viewer frontend URL}
admin_url=${KADRROOM_E2E_ADMIN_URL:?Expected the admin frontend URL}
mail_url=${KADRROOM_E2E_MAIL_URL:?Expected the Mailpit URL}
operator_email=${KADRROOM_E2E_OPERATOR_EMAIL:-operator@example.test}
web_url=${web_url%/}
admin_url=${admin_url%/}
mail_url=${mail_url%/}

scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
response="$scratch/response"
host_cookies="$scratch/host.cookies"
operator_cookies="$scratch/operator.cookies"
guest_cookies="$scratch/guest.cookies"
fixture="$root/apps/frontend/public/demo-media/blue-light.m4a"

fail() {
    printf '%s\n' "$1" >&2
    exit 1
}

expect_status() {
    local expected=$1 action=$2 status
    shift 2
    status=$(curl --silent --connect-timeout 2 --max-time 12 --output "$response" \
        --write-out '%{http_code}' "$@") || fail "Request failed during $action"
    [[ "$status" == "$expected" ]] || fail "Expected HTTP $expected during $action, got $status"
}

response_value() {
    local action=$1
    shift
    jq -er "$@" "$response" 2>/dev/null || fail "Unexpected response during $action"
}

register_account() {
    local email=$1 origin=$2 cookies=$3 role=$4
    local deadline message_id='' token password

    jq -nc --arg email "$email" '{email: $email}' |
        expect_status 202 'requesting confirmation mail' -H "Origin: $web_url" \
            -H 'Content-Type: application/json' --data-binary @- "$web_url/v1/auth/register"

    deadline=$((SECONDS + 30))
    while (( SECONDS < deadline )); do
        expect_status 200 'looking up confirmation mail' --get \
            --data-urlencode "query=to:$email" "$mail_url/api/v1/search"
        message_id=$(response_value 'reading the Mailpit search result' --arg email "$email" "
            [.messages[] | select(any(.To[]; .Address == \$email)) |
                select(.Subject == \"Подтвердите почту для KadrRoom\") | .ID][0] // \"\"
        ")
        [[ -z "$message_id" ]] || break
        sleep 1
    done
    [[ -n "$message_id" ]] || fail 'Mailpit did not receive a confirmation message within 30 seconds'
    message_id=$(jq -nr --arg value "$message_id" '$value | @uri')
    expect_status 200 'fetching confirmation mail' "$mail_url/api/v1/message/$message_id"
    token=$(response_value 'reading the confirmation message' --arg prefix "$web_url/verify?token=" "
        .Text | split(\"\n\") | map(gsub(\"\r\"; \"\") | select(startswith(\$prefix)) |
            ltrimstr(\$prefix) | select(test(\"^[A-Za-z0-9_-]{43}\$\"))) | .[0]
    ")

    password="KadrRoom-$(openssl rand -hex 24)"
    jq -nc --arg token "$token" --arg password "$password" \
        '{token: $token, password: $password, displayName: "KadrRoom CI"}' |
        expect_status 204 'confirming the account' -H "Origin: $web_url" \
            -H 'Content-Type: application/json' --data-binary @- "$web_url/v1/auth/verify"
    jq -nc --arg email "$email" --arg password "$password" '{email: $email, password: $password}' |
        expect_status 200 'signing in' -H "Origin: $origin" -c "$cookies" \
            -H 'Content-Type: application/json' --data-binary @- "$origin/v1/auth/login"
    response_value 'verifying the account role' --arg role "$role" ".role == \$role" >/dev/null
}

bash "$root/tools/ci/wait-testing-stack.sh" "$web_url" "$mail_url" "$admin_url"
expect_status 200 'loading the viewer frontend' "$web_url/"
expect_status 404 'checking isolation of admin routes on the viewer host' "$web_url/v1/admin/catalog"
run_id=$(openssl rand -hex 12)
register_account "viewer-ci-$run_id@example.test" "$web_url" "$host_cookies" user
expect_status 403 'checking catalog management permissions' -b "$host_cookies" "$admin_url/v1/admin/catalog"
register_account "$operator_email" "$admin_url" "$operator_cookies" operator
printf '%s\n' 'Confirmed viewer and operator accounts through Mailpit and checked their roles'

expect_status 201 'uploading demo audio' -H "Origin: $admin_url" -b "$operator_cookies" \
    --form-string "title=KadrRoom CI $run_id" --form-string 'mediaType=audio' \
    -F "file=@$fixture;type=audio/mp4" "$admin_url/v1/admin/catalog"
media_id=$(response_value 'reading the upload identifier' '.id | select(test("^[a-f0-9-]{36}$"))')
deadline=$((SECONDS + 60))
media_status=''
while (( SECONDS < deadline )); do
    expect_status 200 'checking media processing' -b "$operator_cookies" "$admin_url/v1/admin/catalog"
    media_status=$(response_value 'reading the uploaded media status' --arg id "$media_id" ".items[] | select(.id == \$id) | .status")
    [[ "$media_status" != failed ]] || fail 'The media worker failed to process the uploaded audio'
    [[ "$media_status" != ready ]] || break
    sleep 1
done
[[ "$media_status" == ready ]] || fail 'The media worker did not finish within 60 seconds'

expect_status 200 'loading the public catalog before publication' "$web_url/v1/catalog"
response_value 'checking draft exclusion' --arg id "$media_id" ".items | all(.id != \$id)" >/dev/null
expect_status 200 'publishing the processed media' -X POST -H "Origin: $admin_url" \
    -b "$operator_cookies" "$admin_url/v1/admin/catalog/$media_id/publish"
expect_status 200 'loading the public catalog after publication' "$web_url/v1/catalog"
response_value 'checking public media visibility' --arg id "$media_id" \
    ".items | any(.id == \$id and .available and .durationSeconds > 0)" >/dev/null
printf '%s\n' 'Processed and published demo audio through the admin frontend and media worker'

jq -nc --arg id "$media_id" '{mediaId: $id}' |
    expect_status 201 'creating a room for the uploaded media' -H "Origin: $web_url" \
        -H 'Content-Type: application/json' -b "$host_cookies" -c "$host_cookies" \
        --data-binary @- "$web_url/v1/rooms"
room_id=$(response_value 'reading the host room' '. | select(.role == "host") | .roomId | select(test("^[A-Za-z0-9_-]+$"))')
invite=$(response_value 'reading the room invitation' '.invite | select(length > 0)')
jq -nc --arg invite "$invite" '{invite: $invite, name: "KadrRoom CI Guest"}' |
    expect_status 200 'joining the room as a guest' -H "Origin: $web_url" \
        -H 'Content-Type: application/json' -c "$guest_cookies" --data-binary @- "$web_url/v1/join"
response_value 'checking guest membership' --arg id "$room_id" ".roomId == \$id and .role == \"guest\"" >/dev/null
expect_status 200 'issuing a playback link for the guest' -b "$guest_cookies" "$web_url/v1/rooms/$room_id/playback-url"
playback_url=$(response_value 'validating the signed playback URL' --arg prefix "$web_url/v1/media/$media_id?" \
    "select(.mediaType == \"audio\") | .url | select(startswith(\$prefix))")
expect_status 206 'fetching a byte range from the processed audio' -H 'Range: bytes=0-7' "$playback_url"
[[ $(wc -c < "$response") -eq 8 ]] || fail 'The signed audio range had an unexpected length'

if [[ ${KADRROOM_E2E_RTC:-0} == 1 ]]; then
    expect_status 200 'creating the LiveKit room and issuing a guest token' -X POST \
        -H "Origin: $web_url" -b "$guest_cookies" "$web_url/v1/rooms/$room_id/rtc-token"
    response_value 'checking LiveKit token and signaling URL' '
        (.url | test("^wss?://")) and (.token | test("^[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+$"))
    ' >/dev/null
    printf '%s\n' 'Created the LiveKit room and checked RTC token issuance'
fi

expect_status 204 'closing the test room' -X POST -H "Origin: $web_url" \
    -b "$host_cookies" "$web_url/v1/rooms/$room_id/close"
expect_status 200 'unpublishing the processed media' -X POST -H "Origin: $admin_url" \
    -b "$operator_cookies" "$admin_url/v1/admin/catalog/$media_id/unpublish"
expect_status 200 'loading the public catalog after unpublishing' "$web_url/v1/catalog"
response_value 'checking unpublishing in the public catalog' --arg id "$media_id" ".items | all(.id != \$id)" >/dev/null
expect_status 404 'checking revocation of the signed playback URL' "$playback_url"
printf '%s\n' 'Created a room, joined as a guest, played a byte range, and verified unpublishing'
