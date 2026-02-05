FROM golang:1.23-alpine AS builder

WORKDIR /app

# Install build dependencies for CGO (SQLite)
RUN apk add --no-cache gcc musl-dev sqlite-dev

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build with CGO enabled for SQLite support
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o server ./cmd/server

# Runtime stage
FROM alpine:3.21

RUN apk add --no-cache ca-certificates sqlite-libs

# Copy the binary
COPY --from=builder /app/server /server

# Create a non-root user and ensure writable data directory
RUN adduser -D -u 1000 appuser \
  && mkdir -p /home/appuser/.data \
  && chown -R appuser:appuser /home/appuser
USER appuser

WORKDIR /home/appuser

EXPOSE 3000

ENTRYPOINT ["/server"]
