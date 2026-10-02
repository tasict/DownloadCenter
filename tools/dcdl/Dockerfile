# Build environment for dc-dl (musl, static): the dc-bt one plus pkgconf
# for curl's configure. Cross toolchains for ARM are mounted from
# tools/cache/src.
FROM alpine:3.20
RUN apk add --no-cache build-base cmake ninja linux-headers perl xz tar pkgconf
