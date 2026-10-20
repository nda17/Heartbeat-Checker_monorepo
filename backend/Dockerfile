FROM golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p /out/asn-cache \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/probe ./cmd/probe \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# The CI-only binary exercises the real socket path under the production UID.
FROM build AS probe-test-build
RUN CGO_ENABLED=0 go test -c -o /out/probe.test ./internal/probe

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS runtime
ARG VCS_REF=unknown
LABEL org.opencontainers.image.revision=$VCS_REF
ENV APP_ENV=production
USER 65532:65532

FROM runtime AS api
COPY --from=build --chown=65532:65532 /out/api /api
EXPOSE 8080
ENTRYPOINT ["/api"]

FROM runtime AS worker
COPY --from=build --chown=65532:65532 /out/asn-cache /var/lib/heartbeat/asn
COPY --from=build --chown=65532:65532 /out/worker /worker
ENTRYPOINT ["/worker"]

FROM runtime AS probe
COPY --from=build --chown=65532:65532 /out/probe /probe
EXPOSE 8090
ENTRYPOINT ["/probe"]

FROM runtime AS migrate
COPY --from=build --chown=65532:65532 /out/migrate /migrate
ENTRYPOINT ["/migrate"]

FROM runtime AS probe-test
COPY --from=probe-test-build --chown=65532:65532 /out/probe.test /probe.test
ENTRYPOINT ["/probe.test"]
