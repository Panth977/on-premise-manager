# Built by e2e/apps/build.sh: binaries are cross-compiled on the host (CGO_ENABLED=0) into dist/<arch>/.
FROM scratch
ARG TARGETARCH
COPY dist/${TARGETARCH}/hello-web /hello-web
ENV PORT=8080 THEME=green API_BASE=/api
EXPOSE 8080
ENTRYPOINT ["/hello-web"]
