FROM golang:1.25 AS builder

WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags="-s -w" -o /plugin/plugin ./cmd/plugin

FROM alpine:3.21
COPY --from=builder /plugin /plugin
USER 65532:65532
ENTRYPOINT ["/plugin/plugin"]
