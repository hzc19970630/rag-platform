FROM golang:1.26-alpine AS build
# The module proxy must be selectable at build time: the default proxy is not
# reachable from every network, and a failed download otherwise stalls the
# whole image build.
ARG GOPROXY=https://goproxy.cn,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN GOPROXY=${GOPROXY} go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/api ./cmd/api && \
    CGO_ENABLED=0 go build -o /out/worker ./cmd/worker && \
    CGO_ENABLED=0 go build -o /out/migrate ./cmd/migrate && \
    CGO_ENABLED=0 go build -o /out/seed ./cmd/seed && \
    CGO_ENABLED=0 go build -o /out/embedctl ./cmd/embedctl

FROM alpine:3.22
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
