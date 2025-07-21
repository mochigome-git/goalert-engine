# Stage 1: Build the Go program
FROM golang:1.24.3-alpine AS builder
WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git gcc musl-dev

# Copy only the dependency files first 
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o goalert-engine .

# Stage 2: Runtime image
FROM alpine:latest
RUN apk add --no-cache ca-certificates

# Set working directory
WORKDIR /app

# Copy the binary from builder stage 
COPY --from=builder /build/goalert-engine .

# Command to run the application
CMD ["./goalert-engine"]


# Build Image with command
# docker build --no-cache -t goalert-engine:0.2v .
# docker tag goalert-engine:0.2v mochigome/goalert-engine:0.2v
# docker push mochigome/goalert-engine:0.2v

# current version: 0.2v