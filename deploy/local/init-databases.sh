#!/bin/sh
set -eu

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres <<'SQL'
\getenv identity_password KADRROOM_IDENTITY_DB_PASSWORD
\getenv rooms_password KADRROOM_ROOMS_DB_PASSWORD
\getenv catalog_password KADRROOM_CATALOG_DB_PASSWORD
\getenv catalog_read_password KADRROOM_CATALOG_READ_DB_PASSWORD
\getenv catalog_worker_password KADRROOM_CATALOG_WORKER_DB_PASSWORD
SELECT format('CREATE ROLE identity LOGIN PASSWORD %L', :'identity_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'identity') \gexec
SELECT 'CREATE DATABASE identity OWNER identity'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'identity') \gexec
SELECT format('CREATE ROLE rooms LOGIN PASSWORD %L', :'rooms_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'rooms') \gexec
SELECT 'CREATE DATABASE rooms OWNER rooms'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'rooms') \gexec
REVOKE CONNECT ON DATABASE identity, rooms FROM PUBLIC;
GRANT CONNECT ON DATABASE identity TO identity;
GRANT CONNECT ON DATABASE rooms TO rooms;
\if :{?catalog_password}
SELECT format('CREATE ROLE catalog LOGIN PASSWORD %L', :'catalog_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'catalog') \gexec
SELECT 'CREATE DATABASE catalog OWNER catalog'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'catalog') \gexec
\if :{?catalog_read_password}
SELECT format('CREATE ROLE catalog_read LOGIN PASSWORD %L', :'catalog_read_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'catalog_read') \gexec
SELECT format('CREATE ROLE catalog_worker LOGIN PASSWORD %L', :'catalog_worker_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'catalog_worker') \gexec
REVOKE CONNECT ON DATABASE catalog FROM PUBLIC;
GRANT CONNECT ON DATABASE catalog TO catalog, catalog_read, catalog_worker;
\connect catalog
GRANT USAGE ON SCHEMA public TO catalog_read, catalog_worker;
\endif
\endif
SQL

if [ -d /var/lib/kadrroom/media ]; then
    chown 65532:65532 /var/lib/kadrroom/media
fi
