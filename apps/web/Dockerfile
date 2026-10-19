FROM node:24.21.0-bookworm-slim@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20 AS dependencies

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM dependencies AS build
COPY . .
ARG WEB_MODE=app
ARG GO_API_ORIGIN
ARG WEB_PUBLIC_ORIGIN
ARG NEXT_PUBLIC_TURNSTILE_SITE_KEY
ENV WEB_MODE=$WEB_MODE \
    GO_API_ORIGIN=$GO_API_ORIGIN \
    WEB_PUBLIC_ORIGIN=$WEB_PUBLIC_ORIGIN \
    NEXT_PUBLIC_TURNSTILE_SITE_KEY=$NEXT_PUBLIC_TURNSTILE_SITE_KEY
RUN test -n "$WEB_PUBLIC_ORIGIN" \
    && { case "$WEB_MODE" in \
      app) test -n "$GO_API_ORIGIN" && test -n "$NEXT_PUBLIC_TURNSTILE_SITE_KEY" ;; \
      frontend) test -z "$GO_API_ORIGIN" && test -n "$NEXT_PUBLIC_TURNSTILE_SITE_KEY" ;; \
      landing) test -z "$GO_API_ORIGIN" ;; \
      *) exit 1 ;; \
    esac; } \
    && mkdir -p public \
    && npm run build

FROM node:24.21.0-bookworm-slim@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20 AS runtime
WORKDIR /app
ARG VCS_REF=unknown
ARG ANDROID_MANIFEST_SHA256=none
ARG WEB_MODE=app
LABEL org.opencontainers.image.revision=$VCS_REF \
      org.heartbeat-checker.web-mode=$WEB_MODE \
      org.heartbeat-checker.android-manifest-sha256=$ANDROID_MANIFEST_SHA256
ENV NODE_ENV=production PORT=3000 HOSTNAME=0.0.0.0 WEB_MODE=$WEB_MODE
COPY --from=build --chown=node:node /app/.next/standalone ./
COPY --from=build --chown=node:node /app/.next/static ./.next/static
COPY --from=build --chown=node:node /app/public ./public
USER node
EXPOSE 3000
CMD ["node", "server.js"]
