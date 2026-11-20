set -euo pipefail

if (( $# == 0 )); then
    printf '%s\n' 'Usage: check-go-format.sh <path> [<path> ...]' >&2
    exit 2
fi

unformatted=$(gofmt -l "$@")
if [[ -n "$unformatted" ]]; then
    printf '%s\n' "$unformatted" >&2
    exit 1
fi
