FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod .
COPY *.go .
RUN go build -o nfs-tester .

FROM alpine:3.19

RUN apk add --no-cache ca-certificates && \
    addgroup -g 1000 apps && \
    adduser -u 1000 -G apps -D -h /home/apps apps

COPY --from=builder /app/nfs-tester /usr/local/bin/nfs-tester

USER 1000:1000

ENV NFS_PATH=/mnt/nfs
ENV LISTEN_ADDR=:8080

EXPOSE 8080

CMD ["nfs-tester"]
