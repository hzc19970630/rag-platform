FROM golang:1.26-alpine@sha256:87c65a688975c0b1d4585d204874c45ca78354670e3a5f11368c5a492303d696 AS build
# The module proxy must be selectable at build time: the default proxy is not
# reachable from every network, and a failed download otherwise stalls the
# whole image build.
ARG GOPROXY=https://goproxy.cn,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN GOPROXY=${GOPROXY} go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-buildid=' -o /out/api ./cmd/api && \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-buildid=' -o /out/worker ./cmd/worker && \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-buildid=' -o /out/migrate ./cmd/migrate && \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-buildid=' -o /out/seed ./cmd/seed && \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-buildid=' -o /out/embedctl ./cmd/embedctl

FROM alpine:3.22@sha256:bfe7ceb9f09ec0566c48fb93356e45a47a00be5a1dd14fa5b909e7709c66b034
WORKDIR /app
RUN adduser -D -u 10001 app
COPY --from=build /out/api /usr/local/bin/api
COPY --from=build /out/worker /usr/local/bin/worker
COPY --from=build /out/migrate /usr/local/bin/migrate
COPY --from=build /out/seed /usr/local/bin/seed
COPY --from=build /out/embedctl /usr/local/bin/embedctl
COPY migrations /app/migrations
COPY config /app/config
RUN chown -R app:app /app
USER app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
