# Build environment for dc-bt (musl, static). Cross toolchains for ARM are
# mounted from tools/cache/src. The base image is pinned by digest and the
# compiler packages by version (apk's ~ takes later -rN rebuilds of the
# same version), so a rebuild uses the same toolchain.
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
RUN apk add --no-cache build-base~0.5 gcc~13.2.1 g++~13.2.1 musl-dev~1.2.5 binutils~2.42 \
	fortify-headers~1.1 linux-headers~6.6 cmake~3.29 ninja perl xz tar
