set -euo pipefail

base_url=${1:?Expected the service URL}
if [[ "${2:-}" != --via-frontend ]]; then
    for path in /healthz /readyz; do
        curl --fail --silent --show-error --max-time 5 "$base_url$path" \
            | jq -e '.status == "ok"' > /dev/null
    done
fi

curl --fail --silent --show-error --max-time 5 --retry 3 --retry-delay 1 --retry-all-errors \
    "$base_url/v1/catalog" \
    | jq -e '
        .items | type == "array" and length > 0 and all(.[];
            (.id | type == "string" and length > 0)
            and (.title | type == "string" and length > 0)
            and (.description | type == "string")
            and (.durationSeconds | type == "number" and . >= 0)
            and (.available | type == "boolean")
        )
    ' > /dev/null

status=$(curl --silent --show-error --max-time 5 --output /dev/null --write-out '%{http_code}' \
    "$base_url/v1/missing")
if [[ "$status" != 404 ]]; then
    printf 'Expected HTTP 404 for /v1/missing from %s, got %s\n' "$base_url" "$status" >&2
    exit 1
fi
