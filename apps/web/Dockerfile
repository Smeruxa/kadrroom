FROM node:24-bookworm-slim AS build

WORKDIR /source
COPY package*.json tsconfig.base.json ./
COPY apps/web/package.json apps/web/package.json
COPY packages/contracts packages/contracts
COPY tools/ci/install-web.sh tools/ci/install-web.sh
RUN bash tools/ci/install-web.sh

COPY apps/web apps/web
RUN npm run build --workspace apps/web

FROM nginxinc/nginx-unprivileged:stable-alpine

COPY apps/web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /source/apps/web/dist/browser /usr/share/nginx/html

EXPOSE 8080
