FROM golang:1.27.3-alpine AS builder

WORKDIR /build
COPY go.mod ./
COPY . .
ARG BUILD_COMMIT=unknown
ARG BUILD_TIME=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.buildCommit=${BUILD_COMMIT} -X main.buildTime=${BUILD_TIME}" -o cline-proxy .

FROM alpine:3.24

RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /build/cline-proxy .

EXPOSE 3457

ENV PORT=3457

HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
  CMD wget -q -O /dev/null "http://127.0.0.1:${PORT}/health" || exit 1

ENTRYPOINT ["/app/cline-proxy"]
