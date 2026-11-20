FROM node:24-bookworm-slim AS build

WORKDIR /source
COPY package.json package-lock.json tsconfig.base.json ./
COPY apps/frontend/package.json apps/frontend/package.json
COPY apps/admin-frontend/package.json apps/admin-frontend/package.json
COPY packages/contracts packages/contracts
COPY packages/ui packages/ui
COPY tools/ci/install-frontend.sh tools/ci/install-frontend.sh
RUN bash tools/ci/install-frontend.sh

COPY apps/admin-frontend apps/admin-frontend
RUN npm run build --workspace apps/admin-frontend

FROM nginx/nginx-unprivileged:stable-alpine

COPY apps/admin-frontend/nginx.conf /etc/nginx/templates/nginx.conf.template
COPY apps/frontend/nginx-api.conf /etc/nginx/templates/kadrroom-api.conf.template
COPY apps/frontend/nginx-proxy-context.conf /etc/nginx/templates/kadrroom-proxy-context.conf.template
COPY --from=build /source/apps/admin-frontend/dist/browser /usr/share/nginx/html

ENV IDENTITY_API_UPSTREAM=identity-api:8081 \
    ADMIN_API_UPSTREAM=admin-api:8083 \
    CATALOG_API_UPSTREAM=catalog-api:8080 \
    KADRROOM_AUTH_PROXY_TOKEN="" \
    KADRROOM_TRUSTED_PROXY_CIDR=unix: \
    NGINX_ENTRYPOINT_LOCAL_RESOLVERS=1 \
    NGINX_ENVSUBST_OUTPUT_DIR=/tmp \
    NGINX_ENVSUBST_FILTER="^(IDENTITY_API_UPSTREAM|ADMIN_API_UPSTREAM|CATALOG_API_UPSTREAM|KADRROOM_AUTH_PROXY_TOKEN|KADRROOM_TRUSTED_PROXY_CIDR|NGINX_LOCAL_RESOLVERS)$"

USER 101:101
EXPOSE 8080

CMD ["nginx", "-c", "/tmp/nginx.conf", "-g", "daemon off;"]
