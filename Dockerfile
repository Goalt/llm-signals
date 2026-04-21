FROM golang:1.24-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/tg-channel-to-rss ./cmd/server

FROM debian:bookworm-slim

ENV PORT=8000
WORKDIR /app
COPY --from=builder /out/tg-channel-to-rss /app/tg-channel-to-rss

EXPOSE 8000
CMD ["/app/tg-channel-to-rss"]
