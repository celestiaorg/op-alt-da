FROM golang:1.26-alpine3.23 AS builder

WORKDIR /
COPY . op-alt-da
RUN apk add --no-cache make ca-certificates && update-ca-certificates
WORKDIR /op-alt-da
RUN make da-server 

FROM alpine:3.23

# wget for internal healthcheck
RUN apk add --no-cache ca-certificates wget && update-ca-certificates

COPY --from=builder /op-alt-da/bin/da-server /usr/local/bin/da-server

EXPOSE 3100
ENTRYPOINT ["/usr/local/bin/da-server"]
