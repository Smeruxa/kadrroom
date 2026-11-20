set -euo pipefail

base_url=${1:?Expected the service URL}
index=$(curl --fail --silent --show-error --max-time 10 "$base_url/")

if ! grep -Fq '<kadrroom-root' <<< "$index"; then
    printf '%s\n' 'The Angular root component is missing from index.html' >&2
    exit 1
fi

if ! bundles=$(grep -oE 'src="[^"]+\.js"' <<< "$index" | cut -d '"' -f 2); then
    printf '%s\n' 'No JavaScript bundles were found in index.html' >&2
    exit 1
fi

while IFS= read -r bundle; do
    content_type=$(curl --fail --silent --show-error --max-time 10 --output /dev/null \
        --write-out '%{content_type}' "$base_url/${bundle#/}")
    if [[ "$content_type" != application/javascript* && "$content_type" != text/javascript* ]]; then
        printf 'Unexpected content type for %s: %s\n' "$bundle" "$content_type" >&2
        exit 1
    fi
done <<< "$bundles"
