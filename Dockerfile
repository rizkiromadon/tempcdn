FROM golang:1.22-bookworm AS builder

WORKDIR /build

COPY go.mod ./
COPY go.sum* ./

COPY . .

RUN if [ -s go.sum ]; then \
      go mod download; \
    else \
      go mod tidy && go mod download; \
    fi

RUN CGO_ENABLED=0 GOOS=linux go build -o /build/tempcdn-server ./cmd/server

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    wget \
    && rm -rf /var/lib/apt/lists/*

RUN useradd -r -u 1001 -m -d /data tempcdn

WORKDIR /app

COPY --from=builder /build/tempcdn-server /app/tempcdn-server

RUN mkdir -p /data && chown -R tempcdn:tempcdn /data /app

USER tempcdn

ENV SERVER_PORT=8080
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:8080/healthz || exit 1

ENTRYPOINT ["/app/tempcdn-server"]
