#!/bin/sh
# Runs inside the dcdl-builder container: builds zlib, nghttp2, libssh2,
# libcurl and dc-dl for one target, statically against musl. OpenSSL comes
# from the dc-bt build tree (built here when missing).
#   /work/src    pinned source archives and toolchains (read-only)
#   /work/dcdl   dc-dl source (read-only)
#   /work/build  build tree shared with dc-bt (kept between runs)
#   /work/out    result: dc-dl-<target>
# Usage: build-in-container.sh x86_64|arm_64|arm-x41
set -e
T="$1"
VER_SSL=3.5.9
VER_Z=1.3.2
VER_NGHTTP2=1.70.0
VER_SSH2=1.11.1
VER_CURL=8.22.0
B=/work/build/$T
P=$B/prefix
mkdir -p "$B" "$P"
JOBS=$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 2)

case "$T" in
	x86_64)
		CROSS=""
		HOST=""
		SSL_TARGET=linux-x86_64
		;;
	arm_64)
		[ -d /work/build/tc/aarch64-linux-musl-cross ] || { mkdir -p /work/build/tc && tar -xzf /work/src/aarch64-linux-musl-cross.tgz -C /work/build/tc; }
		CROSS=/work/build/tc/aarch64-linux-musl-cross/bin/aarch64-linux-musl-
		HOST=aarch64-linux-musl
		SSL_TARGET=linux-aarch64
		;;
	arm-x41)
		[ -d /work/build/tc/armv7l-linux-musleabihf-cross ] || { mkdir -p /work/build/tc && tar -xzf /work/src/armv7l-linux-musleabihf-cross.tgz -C /work/build/tc; }
		CROSS=/work/build/tc/armv7l-linux-musleabihf-cross/bin/armv7l-linux-musleabihf-
		HOST=armv7l-linux-musleabihf
		SSL_TARGET=linux-armv4
		;;
	*) echo "unknown target $T"; exit 2 ;;
esac
CC=${CROSS}gcc
CXX=${CROSS}g++
AR=${CROSS}ar
RANLIB=${CROSS}ranlib
ARCHFLAGS=""
[ "$T" = arm-x41 ] && ARCHFLAGS="-march=armv7-a -mfpu=vfpv3-d16 -mfloat-abi=hard"
HOSTOPT=""
[ -n "$HOST" ] && HOSTOPT="--host=$HOST"
export CC CXX AR RANLIB
export CFLAGS="-O2 $ARCHFLAGS"
# Only the prefix's libraries, never the build container's
export PKG_CONFIG_LIBDIR="$P/lib/pkgconfig"
export PKG_CONFIG_PATH=""

# OpenSSL (shared with dc-bt)
if [ ! -f "$P/lib/libssl.a" ]; then
	rm -rf "$B/openssl-$VER_SSL"
	tar -xzf /work/src/openssl-$VER_SSL.tar.gz -C "$B"
	cd "$B/openssl-$VER_SSL"
	./Configure "$SSL_TARGET" no-shared no-tests no-docs no-apps no-module no-dso no-engine no-legacy \
		--prefix="$P" --libdir=lib --openssldir=/etc/ssl
	make -j"$JOBS" build_libs >/dev/null
	make install_dev >/dev/null
fi

# zlib
if [ ! -f "$P/lib/libz.a" ]; then
	rm -rf "$B/zlib-$VER_Z"
	tar -xJf /work/src/zlib-$VER_Z.tar.xz -C "$B"
	cd "$B/zlib-$VER_Z"
	./configure --static --prefix="$P" >/dev/null
	make -j"$JOBS" >/dev/null
	make install >/dev/null
fi

# nghttp2 (library only)
if [ ! -f "$P/lib/libnghttp2.a" ]; then
	rm -rf "$B/nghttp2-$VER_NGHTTP2"
	tar -xJf /work/src/nghttp2-$VER_NGHTTP2.tar.xz -C "$B"
	cd "$B/nghttp2-$VER_NGHTTP2"
	./configure $HOSTOPT --prefix="$P" --libdir="$P/lib" --enable-lib-only --disable-shared --enable-static >/dev/null
	make -j"$JOBS" >/dev/null
	make install >/dev/null
fi

# libssh2 (OpenSSL crypto)
if [ ! -f "$P/lib/libssh2.a" ]; then
	rm -rf "$B/libssh2-$VER_SSH2"
	tar -xJf /work/src/libssh2-$VER_SSH2.tar.xz -C "$B"
	cd "$B/libssh2-$VER_SSH2"
	./configure $HOSTOPT --prefix="$P" --libdir="$P/lib" --disable-shared --enable-static --with-crypto=openssl \
		--with-libssl-prefix="$P" --with-libz --with-libz-prefix="$P" --disable-examples-build --disable-docker-tests >/dev/null
	make -j"$JOBS" >/dev/null
	make install >/dev/null
fi

# libcurl: only the protocols Download Center offers
if [ ! -f "$P/lib/libcurl.a" ]; then
	rm -rf "$B/curl-$VER_CURL"
	tar -xJf /work/src/curl-$VER_CURL.tar.xz -C "$B"
	cd "$B/curl-$VER_CURL"
	./configure $HOSTOPT --prefix="$P" --libdir="$P/lib" --disable-shared --enable-static \
		--with-openssl="$P" --with-nghttp2="$P" --with-libssh2="$P" --with-zlib="$P" \
		--without-libpsl --without-brotli --without-zstd --without-libidn2 --without-libgsasl \
		--without-ca-bundle --without-ca-path \
		--disable-ldap --disable-ldaps --disable-rtsp --disable-dict --disable-telnet --disable-tftp \
		--disable-pop3 --disable-imap --disable-smb --disable-smtp --disable-gopher --disable-mqtt \
		--disable-file --disable-ipfs --disable-websockets --disable-manual --disable-docs \
		--enable-ipv6 --enable-threaded-resolver >/dev/null
	make -j"$JOBS" -C lib >/dev/null
	make -C lib install >/dev/null
	make -C include install >/dev/null
	make install-pkgconfigDATA >/dev/null
fi

# dc-dl
rm -rf "$B/dcdl"
mkdir -p "$B/dcdl"
cd "$B/dcdl"
LIBS=$(pkg-config --static --libs libcurl)
$CXX -std=c++17 -O2 $ARCHFLAGS -Wall -I"$P/include" -I/work/src -DCURL_STATICLIB \
	/work/dcdl/dc-dl.cpp -o dc-dl -L"$P/lib" $LIBS -static -no-pie -s -pthread
cp -f dc-dl /work/out/dc-dl-$T
echo "built /work/out/dc-dl-$T"
