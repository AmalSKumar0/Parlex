# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git ca-certificates tzdata

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s -extldflags '-static'" \
    -o /bin/server ./cmd/server

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s -extldflags '-static'" \
    -o /bin/simulator ./cmd/simulator

# Final distroless non-root image
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=builder /bin/server /app/server
COPY --from=builder /bin/simulator /app/simulator
COPY --from=builder /app/config/languages.yaml /app/config/languages.yaml
COPY --from=builder /app/migrations /app/migrations

USER nonroot:nonroot

EXPOSE 8080 9090

ENTRYPOINT ["/app/server"]
