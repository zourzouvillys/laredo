# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/laredo-server ./cmd/laredo-server && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/laredo ./cmd/laredo

# Production image (distroless)
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /bin/laredo-server /usr/local/bin/laredo-server
COPY --from=builder /bin/laredo /usr/local/bin/laredo

# Default config path
ENV LAREDO_CONFIG=/etc/laredo/laredo.conf

# gRPC (OAM + Query)
EXPOSE 4001
# Health + Metrics HTTP
EXPOSE 8080

# Volumes for config and data
VOLUME ["/etc/laredo", "/etc/laredo/conf.d", "/var/lib/laredo"]

# Health check using the built-in healthcheck subcommand
HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=3 \
    CMD ["laredo-server", "healthcheck"]

ENTRYPOINT ["laredo-server"]
