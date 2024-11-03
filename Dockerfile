FROM golang:alpine AS builder
RUN apk update && apk add --no-cache ca-certificates && update-ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o mutedeck2mqtt

FROM scratch
WORKDIR /
COPY --from=builder /app/mutedeck2mqtt /mutedeck2mqtt
EXPOSE 8080
ENTRYPOINT ["/mutedeck2mqtt"]