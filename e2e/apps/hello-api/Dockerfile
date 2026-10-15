# Built by e2e/apps/build.sh: binaries are cross-compiled on the host (CGO_ENABLED=0) into dist/<arch>/.
FROM scratch
ARG TARGETARCH
COPY dist/${TARGETARCH}/hello-api /hello-api
ENV PORT=8080 DATA_DIR=/data GREETING=hello
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/hello-api"]
