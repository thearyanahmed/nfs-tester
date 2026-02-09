FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod .
COPY *.go .
RUN go build -o nfs-tester .

FROM alpine:3.19

RUN apk add --no-cache ca-certificates && \
    addgroup -g 2345 testgrp && \
    adduser -u 1234 -G testgrp -D -h /home/testuser testuser

COPY --from=builder /app/nfs-tester /usr/local/bin/nfs-tester

# uid/gid mismatch test: container runs as 1234:2345, VAST impersonates as 1000:1000
USER 1234:2345

ENV NFS_PATH=/mnt/nfs
ENV LISTEN_ADDR=:8080

EXPOSE 8080

CMD ["nfs-tester"]
