# Build stage
FROM golang:1.21-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o gprxy .

# Runtime stage
FROM alpine:latest

# Install ca-certificates for HTTPS (JWKS fetching)
RUN apk --no-cache add ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1000 gprxy && \
    adduser -D -u 1000 -G gprxy gprxy

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/gprxy /app/gprxy

# Change ownership
RUN chown -R gprxy:gprxy /app

# Switch to non-root user
USER gprxy

# Expose proxy port
EXPOSE 7777

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD nc -z localhost 7777 || exit 1

# Run the proxy
ENTRYPOINT ["/app/gprxy"]
CMD ["start"]

