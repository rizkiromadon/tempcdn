FROM golang:1.22-bookworm AS builder

WORKDIR /build

COPY go.mod ./

COPY . .

RUN go mod tidy && go mod download

RUN CGO_ENABLED=1 GOOS=linux go build -o /build/tempcdn-server ./cmd/server

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /build/tempcdn-server /app/tempcdn-server

ENV SERVER_PORT=8080
EXPOSE 8080

ENTRYPOINT ["/app/tempcdn-server"]
