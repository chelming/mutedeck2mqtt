FROM golang:alpine AS builder
RUN apk update && apk add --no-cache ca-certificates && update-ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o mutedeck2mqtt .

FROM scratch
WORKDIR /
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/mutedeck2mqtt /mutedeck2mqtt

EXPOSE 8080
ENTRYPOINT ["/mutedeck2mqtt"]
