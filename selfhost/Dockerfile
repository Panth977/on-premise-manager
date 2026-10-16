# opm-cloud — the OPM cloud on a VM (SPEC §21 #108). Build from the on-premise-manager directory:
#   docker build -f selfhost/Dockerfile -t opm-cloud .
# Stage 1: the Go artefact of the GitHub mirror (the exporter as wasm).
FROM golang:1.26 AS go
WORKDIR /src/agent
COPY agent/go.mod agent/go.sum ./
RUN go mod download
COPY agent/ ./
RUN mkdir -p /out && \
    GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o /out/opm-export.wasm ./cmd/opm-export

# Stage 2: shared contracts, the frontend (static build) and the server bundle.
FROM node:22-bookworm AS build
WORKDIR /src
COPY shared/package.json shared/package-lock.json shared/
RUN cd shared && npm ci
COPY ui/package.json ui/package-lock.json ui/
RUN cd ui && npm ci
COPY frontend/package.json frontend/package-lock.json frontend/
RUN cd frontend && npm ci
COPY backend/package.json backend/package-lock.json backend/
RUN cd backend && npm ci
COPY shared/ shared/
RUN cd shared && npm run build
COPY ui/ ui/
COPY frontend/ frontend/
RUN cd frontend && npm run build
COPY backend/ backend/
COPY --from=go /out/ backend/functions/lib/
# build.mjs would rebuild the Go artefacts (no Go here): write their metadata, then bundle the server
RUN cd backend && OPM_SKIP_GO=1 node build.mjs && node -e "const fs=require('fs'),c=require('crypto');const d='functions/lib';const m={version:'',artefacts:{}};for(const f of ['opm-export.wasm'])m.artefacts[f]=c.createHash('sha256').update(fs.readFileSync(d+'/'+f)).digest('hex');m.version='export-'+m.artefacts['opm-export.wasm'].slice(0,12);fs.writeFileSync(d+'/opm-tools.json',JSON.stringify(m,null,2)+'\n')" && node build-server.mjs

# Stage 3: runtime — Node + the bundle + the static frontend. No Firebase SDK: the local adapter never contacts Google
# (migrating FROM Firebase runs from the repo checkout: backend/scripts/migrate-cloud.ts).
FROM node:22-bookworm-slim
ENV NODE_ENV=production \
    OPM_ADAPTER=local \
    OPM_DATA_DIR=/data \
    OPM_PORT=8790 \
    OPM_STATIC_DIR=/opt/opm-cloud/www
WORKDIR /opt/opm-cloud
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl tini && rm -rf /var/lib/apt/lists/*
COPY --from=build /src/backend/dist/ /opt/opm-cloud/dist/
COPY --from=build /src/frontend/build/ /opt/opm-cloud/www/
RUN useradd --system --uid 10001 --home /data opm && mkdir -p /data && chown opm /data && \
    printf '#!/bin/sh\nexec node --no-warnings=ExperimentalWarning /opt/opm-cloud/dist/opm-cloud.cjs "$@"\n' > /usr/local/bin/opm-cloud && chmod +x /usr/local/bin/opm-cloud
USER opm
VOLUME ["/data"]
EXPOSE 8790
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD curl -fsS http://127.0.0.1:8790/healthz || exit 1
ENTRYPOINT ["tini", "--", "opm-cloud"]
CMD ["serve"]
