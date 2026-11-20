#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

if [[ $# != 3 || ( $1 != test && $1 != production ) ]]; then
    printf 'Usage: bash deploy/k8s/render.sh <test|production> <public-hostname> <admin-hostname>\n' >&2
    exit 2
fi

label='[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?'
for hostname in "$2" "$3"; do
    if [[ ${#hostname} -gt 253 || ! $hostname =~ ^($label\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$ || $hostname == *.invalid ]]; then
        printf 'Expected a lowercase DNS hostname without a scheme, port or path.\n' >&2
        exit 2
    fi
done
if [[ $2 == "$3" ]]; then
    printf 'Public and admin hostnames must be different.\n' >&2
    exit 2
fi

root=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
kubectl kustomize "$root/overlays/$1" | sed \
    -e "s/admin\.kadrroom\.example\.invalid/$3/g" \
    -e "s/kadrroom\.example\.invalid/$2/g"
