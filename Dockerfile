FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/notes ./cmd/notes

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -g 10001 notes && adduser -D -u 10001 -G notes notes
COPY --from=build /out/notes /usr/local/bin/notes
USER notes
ENV SERVICE_PORT=8080
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s CMD wget -q -O /dev/null http://127.0.0.1:${SERVICE_PORT:-8080}/readyz || exit 1
ENTRYPOINT ["/usr/local/bin/notes"]
