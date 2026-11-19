set -euo pipefail

base_url=${1:?Expected the service URL}
curl --fail --silent --show-error --max-time 10 "$base_url/healthz" \
    | jq -e '.status == "ok"' > /dev/null

curl --fail --silent --show-error --max-time 10 "$base_url/v1/catalog" \
    | jq -e '
        .items | type == "array" and length > 0 and all(.[];
            (.id | type == "string" and length > 0)
            and (.title | type == "string" and length > 0)
            and (.description | type == "string")
            and (.durationSeconds | type == "number" and . >= 0)
            and (.available | type == "boolean")
        )
    ' > /dev/null
