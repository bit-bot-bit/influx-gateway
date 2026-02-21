FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -mod=mod -o /out/influx-gateway ./cmd/influx-gateway

FROM alpine:3.20
RUN adduser -D -u 10001 app
USER app
COPY --from=build /out/influx-gateway /usr/local/bin/influx-gateway
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/influx-gateway"]
