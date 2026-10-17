FROM node:24.21.0-bookworm-slim@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20 AS dependencies

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM dependencies AS build
COPY . .
ARG GO_API_ORIGIN
ARG WEB_PUBLIC_ORIGIN
ARG NEXT_PUBLIC_TURNSTILE_SITE_KEY
ENV GO_API_ORIGIN=$GO_API_ORIGIN \
    WEB_PUBLIC_ORIGIN=$WEB_PUBLIC_ORIGIN \
    NEXT_PUBLIC_TURNSTILE_SITE_KEY=$NEXT_PUBLIC_TURNSTILE_SITE_KEY
RUN test -n "$GO_API_ORIGIN" \
    && test -n "$WEB_PUBLIC_ORIGIN" \
    && test -n "$NEXT_PUBLIC_TURNSTILE_SITE_KEY" \
    && mkdir -p public \
    && npm run build

FROM node:24.21.0-bookworm-slim@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20 AS runtime
WORKDIR /app
ARG VCS_REF=unknown
LABEL org.opencontainers.image.revision=$VCS_REF
ENV NODE_ENV=production PORT=3000 HOSTNAME=0.0.0.0
COPY --from=build --chown=node:node /app/.next/standalone ./
COPY --from=build --chown=node:node /app/.next/static ./.next/static
COPY --from=build --chown=node:node /app/public ./public
USER node
EXPOSE 3000
CMD ["node", "server.js"]
