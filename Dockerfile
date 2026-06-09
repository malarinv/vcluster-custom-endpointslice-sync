FROM golang:1.25 AS builder

WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags="-s -w" -o /plugin ./cmd/plugin

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /plugin /plugin
ENTRYPOINT ["/plugin"]
