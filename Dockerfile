FROM golang:1.24-alpine3.22 as builder

WORKDIR /
COPY . op-alt-da
RUN apk add --no-cache make ca-certificates && update-ca-certificates
WORKDIR /op-alt-da
RUN make da-server 

FROM alpine:3.18

RUN apk add --no-cache ca-certificates && update-ca-certificates

COPY --from=builder /op-alt-da/bin/da-server /usr/local/bin/da-server

EXPOSE 3100
ENTRYPOINT ["/usr/local/bin/da-server"]