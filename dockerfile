# Use the official Go image
FROM golang:1.21-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git

# Set working directory
WORKDIR /app

# Copy go.mod and go.sum
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .

# Final stage
FROM alpine:latest

# Install runtime dependencies including Python
RUN apk --no-cache add ca-certificates python3 py3-pip ffmpeg wget

# Install yt-dlp via pip3
RUN pip3 install --break-system-packages yt-dlp

# Make sure yt-dlp is executable and in the right location
RUN which yt-dlp || echo "yt-dlp not found in PATH"
RUN find / -name "yt-dlp" -type f 2>/dev/null || echo "yt-dlp not found anywhere"

# Create symlinks for common locations
RUN ln -sf $(which yt-dlp || echo /usr/bin/yt-dlp) /usr/bin/yt-dlp 2>/dev/null || true
RUN ln -sf $(which yt-dlp || echo /usr/local/bin/yt-dlp) /usr/local/bin/yt-dlp 2>/dev/null || true

# Alternative: Download directly from GitHub
RUN wget -O /usr/local/bin/yt-dlp https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp
RUN chmod +x /usr/local/bin/yt-dlp

# Create additional symlink
RUN ln -sf /usr/local/bin/yt-dlp /usr/bin/yt-dlp

# Test the installation
RUN /usr/bin/yt-dlp --version
RUN /usr/local/bin/yt-dlp --version

WORKDIR /root/

# Copy the binary from builder stage
COPY --from=builder /app/main .

# Create downloads directory
RUN mkdir downloads

# Expose port
EXPOSE 8080

# Run the binary
CMD ["./main"]
