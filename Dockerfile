FROM golang:1.26-alpine@sha256:ecb67f987aa1a57794c91d19ade7ae0209f2f0f5496aaef9e6ae85ddf92d02ab AS build
# The module proxy must be selectable at build time: the default proxy is not
# reachable from every network, and a failed download otherwise stalls the
# whole image build.
ARG GOPROXY=https://goproxy.cn,direct
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN GOPROXY=${GOPROXY} go mod download
COPY . .
RUN LDFLAGS="-buildid= -X github.com/hzc19970630/rag-platform/internal/buildinfo.Version=${VERSION} -X github.com/hzc19970630/rag-platform/internal/buildinfo.Commit=${COMMIT} -X github.com/hzc19970630/rag-platform/internal/buildinfo.Date=${BUILD_DATE}" && \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="$LDFLAGS" -o /out/api ./cmd/api && \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="$LDFLAGS" -o /out/worker ./cmd/worker && \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="$LDFLAGS" -o /out/migrate ./cmd/migrate && \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="$LDFLAGS" -o /out/seed ./cmd/seed && \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="$LDFLAGS" -o /out/embedctl ./cmd/embedctl && \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="$LDFLAGS" -o /out/preset ./cmd/preset

FROM alpine:3.22@sha256:bfe7ceb9f09ec0566c48fb93356e45a47a00be5a1dd14fa5b909e7709c66b034
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.title="rag-platform" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.source="https://github.com/hzc19970630/rag-platform"

# ALPINE-CVE-2026-4738: the pinned 3.22.8 image ships zlib 1.3.2-r1; the
# Alpine v3.22 repository publishes the fixed 1.4-r0.
# CVE-2026-124709: the same base ships openssl 3.5.9-r0; v3.22 publishes 3.5.9-r2.
RUN apk add --no-cache zlib=1.4-r0 libcrypto3=3.5.9-r2 libssl3=3.5.9-r2
WORKDIR /app
RUN adduser -D -u 10001 app
COPY --from=build /out/api /usr/local/bin/api
COPY --from=build /out/worker /usr/local/bin/worker
COPY --from=build /out/migrate /usr/local/bin/migrate
COPY --from=build /out/seed /usr/local/bin/seed
COPY --from=build /out/embedctl /usr/local/bin/embedctl
COPY --from=build /out/preset /usr/local/bin/preset
COPY migrations /app/migrations
COPY config /app/config
RUN chown -R app:app /app
USER app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
