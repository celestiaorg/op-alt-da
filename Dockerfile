FROM golang:1.22.2-alpine3.18 as builder

WORKDIR /
COPY . op-alt-da
RUN apk add --no-cache make
WORKDIR /op-alt-da
RUN make da-server

FROM alpine:3.18

COPY --from=builder /op-alt-da/bin/da-server /usr/local/bin/da-server

EXPOSE 3100
ENTRYPOINT ["/usr/local/bin/da-server"]
