FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY main.go .
RUN go build -o nfs-tester main.go

FROM alpine:3.19

RUN apk add --no-cache ca-certificates

# create user with uid=1234 (different), gid=678 (same as app-998)
RUN addgroup -g 678 nfstest && \
    adduser -D -u 1234 -G nfstest -s /bin/sh nfstest

COPY --from=builder /app/nfs-tester /usr/local/bin/nfs-tester

USER 1234:678

ENV NFS_PATH=/mnt/nfs
ENV LISTEN_ADDR=:8080

EXPOSE 8080

CMD ["nfs-tester"]
