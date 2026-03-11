FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /gateway ./cmd/gateway

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 1000 gateway
COPY --from=builder /gateway /usr/local/bin/gateway
COPY config/gateway.yaml /etc/gateway/gateway.yaml
USER gateway
EXPOSE 8080
ENTRYPOINT ["gateway"]
CMD ["-config", "/etc/gateway/gateway.yaml"]
