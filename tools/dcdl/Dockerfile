# Build environment for dc-dl (musl, static): the dc-bt one plus pkgconf
# for curl's configure. Cross toolchains for ARM are mounted from
# tools/cache/src. Pinned like tools/dcbt/Dockerfile.
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
RUN apk add --no-cache build-base~0.5 gcc~13.2.1 g++~13.2.1 musl-dev~1.2.5 binutils~2.42 \
	fortify-headers~1.1 linux-headers~6.6 cmake~3.29 ninja perl xz tar pkgconf~2.2
