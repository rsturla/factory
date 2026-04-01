FROM quay.io/hummingbird/go:1.26 AS builder

WORKDIR /src
COPY go.mod go.sum ./
COPY pkg/sdk/go.mod pkg/sdk/
RUN go mod download

COPY . .

ARG BINARY
RUN CGO_ENABLED=0 go build -o /app ./cmd/${BINARY}/

FROM quay.io/hummingbird/core-runtime:latest
COPY --from=builder /app /app
ENTRYPOINT ["/app"]
