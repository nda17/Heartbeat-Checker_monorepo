FROM golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
ARG VCS_REF=unknown
WORKDIR /src
# Context is tools/, never the repository root or a local deployment directory.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.sourceSHA=$VCS_REF" -o /out/hc-ops ./cmd/hc-ops

FROM postgres:18.6@sha256:74935e72241653ca55e0414067e6d8763aceb8a810eb51b452253ec3dcfc4336
ARG VCS_REF=unknown
LABEL org.opencontainers.image.revision=$VCS_REF
# Preserve the CA bundle, PostgreSQL programs and age used by the restore proof.
COPY --from=caddy:2.11.7@sha256:685ae3d7c34d9b5f8caeddc5d14051db7bbd88ff1f5a2fa0c16bd8ca328fb7af \
    /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN sed -i 's|URIs: http://deb.debian.org/|URIs: https://deb.debian.org/|' /etc/apt/sources.list.d/debian.sources \
    && sed -i 's|http://apt.postgresql.org/|https://apt.postgresql.org/|' /etc/apt/sources.list.d/pgdg.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends age ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /state \
    && chown postgres:postgres /state
COPY --from=build /out/hc-ops /opt/backup/hc-ops
USER postgres:postgres
ENV BACKUP_STATE_DIR=/state
ENTRYPOINT ["/opt/backup/hc-ops", "backup"]
