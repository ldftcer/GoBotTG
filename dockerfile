# Use the official Go image
FROM golang:1.21-alpine AS builder

# Install dependencies
RUN apk add --no-cache git python3 py3-pip ffmpeg

# Install yt-dlp
RUN pip3 install yt-dlp

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

# Install runtime dependencies
RUN apk --no-cache add ca-certificates python3 py3-pip ffmpeg

# Install yt-dlp in final image
RUN pip3 install yt-dlp

WORKDIR /root/

# Copy the binary from builder stage
COPY --from=builder /app/main .

# Create downloads directory
RUN mkdir downloads

# Expose port (Railway will override this)
EXPOSE 8080

# Run the binary
CMD ["./main"]