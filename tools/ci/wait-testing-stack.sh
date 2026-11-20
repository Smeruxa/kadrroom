set -euo pipefail

web_url=${1:?Expected the frontend base URL}
mail_url=${2:?Expected the Mailpit base URL}
admin_url=${3:?Expected the admin frontend base URL}

wait_for_status() {
    local url=$1 expected=$2 attempt status
    for ((attempt = 1; attempt <= 30; attempt++)); do
        status=$(curl --silent --connect-timeout 1 --max-time 2 --output /dev/null \
            --write-out '%{http_code}' "$url") || status=000
        if [[ "$status" == "$expected" ]]; then
            return
        fi
        sleep 1
    done
    printf 'Expected HTTP %s from %s, last status was %s\n' "$expected" "$url" "$status" >&2
    return 1
}

wait_for_status "$web_url/v1/catalog" 200
wait_for_status "$web_url/v1/auth/session" 401
wait_for_status "$web_url/v1/rooms/ci-readiness" 403
wait_for_status "$mail_url/api/v1/messages" 200
wait_for_status "$admin_url/" 200
wait_for_status "$admin_url/v1/admin/catalog" 401
