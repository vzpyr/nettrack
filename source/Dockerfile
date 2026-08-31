FROM golang:1.24-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /build/nettrack .

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -u 1000 -D nettrack && \
    mkdir -p /data && \
    chown -R nettrack:nettrack /data
COPY --from=builder /build/nettrack /usr/local/bin/nettrack
USER nettrack
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["nettrack"]
