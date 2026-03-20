FROM golang:1.23-alpine AS builder

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/laredo-server ./cmd/laredo-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /bin/laredo-server /usr/local/bin/laredo-server
ENTRYPOINT ["laredo-server"]
