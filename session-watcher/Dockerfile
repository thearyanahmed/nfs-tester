FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod .
COPY main.go .
RUN go build -o session-watcher .

FROM alpine:3.19

RUN apk add --no-cache ca-certificates && \
    addgroup -g 1000 apps && \
    adduser -u 1000 -G apps -D -h /home/apps apps

COPY --from=builder /app/session-watcher /usr/local/bin/session-watcher

USER 1000:1000

ENV SESSION_PATH=/data/sessions
ENV LISTEN_ADDR=:8081

EXPOSE 8081

CMD ["session-watcher"]
