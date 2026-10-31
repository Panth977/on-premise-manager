# opm-cloud — the OPM cloud on a VM (SPEC §21 #108). Build from the on-premise-manager directory:
#   docker build -f selfhost/Dockerfile -t opm-cloud .
# Stage 1: opm-cloud (Go, static).
FROM golang:1.26 AS go
WORKDIR /src/agent
COPY agent/go.mod agent/go.sum ./
RUN go mod download
COPY agent/ ./
RUN mkdir -p /out && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/opm-cloud ./cmd/opm-cloud

# Stage 2: the frontend (static build).
FROM node:22-bookworm AS build
WORKDIR /src
COPY shared/package.json shared/package-lock.json shared/
RUN cd shared && npm ci
COPY ui/package.json ui/package-lock.json ui/
RUN cd ui && npm ci
COPY frontend/package.json frontend/package-lock.json frontend/
RUN cd frontend && npm ci
COPY shared/ shared/
RUN cd shared && npm run build
COPY ui/ ui/
COPY frontend/ frontend/
RUN cd frontend && npm run build

# Stage 3: runtime.
FROM debian:bookworm-slim
ENV OPM_CLOUD_VERSION=0.17.0 \
    OPM_DATA_DIR=/data \
    OPM_PORT=8790 \
    OPM_STATIC_DIR=/opt/opm-cloud/www
WORKDIR /opt/opm-cloud
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl tini && rm -rf /var/lib/apt/lists/*
COPY --from=go /out/opm-cloud /usr/local/bin/opm-cloud
COPY --from=build /src/frontend/build/ /opt/opm-cloud/www/
RUN useradd --system --uid 10001 --home /data opm && mkdir -p /data && chown opm /data
USER opm
VOLUME ["/data"]
EXPOSE 8790
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD curl -fsS http://127.0.0.1:8790/healthz || exit 1
ENTRYPOINT ["tini", "--", "opm-cloud"]
CMD ["serve"]
