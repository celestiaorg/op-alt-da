FROM scratch
COPY op-alt-da /usr/bin/op-alt-da
ENTRYPOINT ["/usr/bin/op-alt-da"]
