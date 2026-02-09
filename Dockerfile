FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod .
COPY *.go .
RUN go build -o nfs-tester .

FROM alpine:3.19

RUN apk add --no-cache ca-certificates

COPY --from=builder /app/nfs-tester /usr/local/bin/nfs-tester

# running as root (uid=0) to test VAST impersonation with gvisor
USER root

ENV NFS_PATH=/mnt/nfs
ENV LISTEN_ADDR=:8080

EXPOSE 8080

CMD ["nfs-tester"]
