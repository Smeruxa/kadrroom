#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
environment=${1:-}
case "$environment" in
    testing)
        web_port=4173
        admin_web_port=4183
        mail_port=8025
        site_host=127.0.0.1
        rtc_port=7880
        ;;
    prod)
        web_port=4174
        admin_web_port=4184
        mail_port=8026
        site_host=localhost
        rtc_port=7890
        ;;
    *)
        printf 'Usage: bash tools/local/stack.sh {testing|prod} <compose arguments>\n' >&2
        exit 2
        ;;
esac
shift

if [[ -n "${KADRROOM_COMPOSE_BIN:-}" ]]; then
    compose=("$KADRROOM_COMPOSE_BIN")
elif docker compose version >/dev/null 2>&1; then
    compose=(docker compose)
else
    compose=(docker-compose)
fi

env_file=${KADRROOM_LOCAL_ENV_FILE:-"$root/.env.$environment.local"}
secret_names=(
    KADRROOM_CATALOG_DB_PASSWORD KADRROOM_CATALOG_READ_DB_PASSWORD KADRROOM_CATALOG_WORKER_DB_PASSWORD
    KADRROOM_CATALOG_INTERNAL_TOKEN KADRROOM_LIVEKIT_API_SECRET
    KADRROOM_MAIL_TOKEN_KEY KADRROOM_AUTH_PROXY_TOKEN
)

has_secret() {
    grep -Eq "^[[:blank:]]*(export[[:blank:]]+)?$1[[:blank:]]*[:=]" "$2"
}

needs_backfill() {
    [[ -f "$env_file" ]] || return 0
    local name
    for name in "${secret_names[@]}"; do
        if ! has_secret "$name" "$env_file"; then
            return 0
        fi
    done
    return 1
}

generate_secret() {
    local bytes=$1 secret
    if ! secret=$(openssl rand -hex "$bytes") ||
        [[ ${#secret} != "$((bytes * 2))" || ! $secret =~ ^[0-9a-f]+$ ]]; then
        printf 'Could not generate a local environment secret.\n' >&2
        return 1
    fi
    printf '%s' "$secret"
}

if needs_backfill; then
    (
        umask 077
        lock_dir="$env_file.lock"
        if ! mkdir -- "$lock_dir"; then
            printf 'Could not lock the local environment file. Retry after the other setup finishes.\n' >&2
            exit 1
        fi
        env_temp=
        trap '
            if [[ -n "$env_temp" ]]; then
                rm -f -- "$env_temp"
            fi
            rmdir -- "$lock_dir"
        ' EXIT
        trap 'exit 1' HUP INT TERM

        if ! needs_backfill; then
            exit 0
        fi
        env_temp=$(mktemp "$env_file.tmp.XXXXXXXX")
        if [[ -e "$env_file" ]]; then
            cat -- "$env_file" > "$env_temp"
            if [[ -s "$env_temp" && -n $(tail -c 1 "$env_temp") ]]; then
                printf '\n' >> "$env_temp"
            fi
        else
            printf 'KADRROOM_WEB_PORT=%s\n' "${KADRROOM_WEB_PORT:-$web_port}" >> "$env_temp"
            printf 'KADRROOM_MAIL_PORT=%s\n' "${KADRROOM_MAIL_PORT:-$mail_port}" >> "$env_temp"
            for name in KADRROOM_DB_PASSWORD KADRROOM_IDENTITY_DB_PASSWORD KADRROOM_ROOMS_DB_PASSWORD \
                KADRROOM_CATALOG_DB_PASSWORD; do
                secret=$(generate_secret 24)
                printf '%s=%s\n' "$name" "$secret" >> "$env_temp"
            done
        fi
        for name in "${secret_names[@]}"; do
            if ! has_secret "$name" "$env_temp"; then
                secret=$(generate_secret 32)
                printf '%s=%s\n' "$name" "$secret" >> "$env_temp"
            fi
        done
        mv -f -- "$env_temp" "$env_file"
        env_temp=
    )
fi

unset KADRROOM_MAIL_TOKEN_KEY KADRROOM_AUTH_PROXY_TOKEN
export KADRROOM_SITE_HOST=${KADRROOM_SITE_HOST:-$site_host}
export KADRROOM_ADMIN_WEB_PORT=${KADRROOM_ADMIN_WEB_PORT:-$admin_web_port}
export KADRROOM_RTC_PORT=${KADRROOM_RTC_PORT:-$rtc_port}
export KADRROOM_RTC_TCP_PORT=${KADRROOM_RTC_TCP_PORT:-$((rtc_port + 1))}
export KADRROOM_RTC_UDP_PORT=${KADRROOM_RTC_UDP_PORT:-$((rtc_port + 2))}

exec "${compose[@]}" --project-name "kadrroom-$environment" --env-file "$env_file" \
    --file "$root/deploy/local/compose.yml" "$@"
