#!/bin/sh
# Runs inside the dcbt-builder container: builds OpenSSL, libtorrent and
# dc-bt for one target, statically against musl.
#   /work/src    pinned source archives and toolchains (read-only)
#   /work/dcbt   dc-bt source (read-only)
#   /work/build  build tree (kept between runs)
#   /work/out    result: dc-bt-<target>
# Usage: build-in-container.sh x86_64|arm_64|arm-x41
set -e
T="$1"
VER_LT=2.0.15
VER_BOOST=1_88_0
VER_SSL=3.5.9
B=/work/build/$T
P=$B/prefix
mkdir -p "$B" "$P"
JOBS=$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 2)

case "$T" in
	x86_64)
		CROSS=""
		SSL_TARGET=linux-x86_64
		CPU=x86_64
		;;
	arm_64)
		[ -d /work/build/tc/aarch64-linux-musl-cross ] || { mkdir -p /work/build/tc && tar -xzf /work/src/aarch64-linux-musl-cross.tgz -C /work/build/tc; }
		CROSS=/work/build/tc/aarch64-linux-musl-cross/bin/aarch64-linux-musl-
		SSL_TARGET=linux-aarch64
		CPU=aarch64
		;;
	arm-x41)
		[ -d /work/build/tc/armv7l-linux-musleabihf-cross ] || { mkdir -p /work/build/tc && tar -xzf /work/src/armv7l-linux-musleabihf-cross.tgz -C /work/build/tc; }
		CROSS=/work/build/tc/armv7l-linux-musleabihf-cross/bin/armv7l-linux-musleabihf-
		SSL_TARGET=linux-armv4
		CPU=armv7
		;;
	*) echo "unknown target $T"; exit 2 ;;
esac
CC=${CROSS}gcc
CXX=${CROSS}g++
AR=${CROSS}ar
RANLIB=${CROSS}ranlib
ARCHFLAGS=""
[ "$T" = arm-x41 ] && ARCHFLAGS="-march=armv7-a -mfpu=vfpv3-d16 -mfloat-abi=hard"

# Boost: headers only (libtorrent 2.0 needs no compiled Boost library)
[ -d /work/build/boost_$VER_BOOST ] || tar -xzf /work/src/boost_$VER_BOOST.tar.gz -C /work/build boost_$VER_BOOST/boost
mkdir -p /work/build/boost_$VER_BOOST/stage

# OpenSSL (libcrypto + libssl, static)
if [ ! -f "$P/lib/libssl.a" ]; then
	rm -rf "$B/openssl-$VER_SSL"
	tar -xzf /work/src/openssl-$VER_SSL.tar.gz -C "$B"
	cd "$B/openssl-$VER_SSL"
	CC="$CC" AR="$AR" RANLIB="$RANLIB" CFLAGS="-O2 $ARCHFLAGS" ./Configure "$SSL_TARGET" no-shared no-tests no-docs no-apps no-module no-dso no-engine no-legacy \
		--prefix="$P" --libdir=lib --openssldir=/etc/ssl
	make -j"$JOBS" build_libs >/dev/null
	make install_dev >/dev/null
fi

# libtorrent (static)
if [ ! -f "$P/lib/libtorrent-rasterbar.a" ]; then
	rm -rf "$B/lt" "$B/libtorrent-rasterbar-$VER_LT"
	tar -xzf /work/src/libtorrent-rasterbar-$VER_LT.tar.gz -C "$B"
	mkdir -p "$B/lt"
	cd "$B/lt"
	cmake -G Ninja "$B/libtorrent-rasterbar-$VER_LT" \
		-DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$P" \
		-DCMAKE_SYSTEM_NAME=Linux -DCMAKE_SYSTEM_PROCESSOR=$CPU \
		-DCMAKE_C_COMPILER="$CC" -DCMAKE_CXX_COMPILER="$CXX" \
		-DCMAKE_C_FLAGS="$ARCHFLAGS" -DCMAKE_CXX_FLAGS="$ARCHFLAGS" \
		-DCMAKE_FIND_ROOT_PATH="$P" -DCMAKE_FIND_ROOT_PATH_MODE_PACKAGE=BOTH -DCMAKE_FIND_ROOT_PATH_MODE_INCLUDE=BOTH -DCMAKE_FIND_ROOT_PATH_MODE_LIBRARY=BOTH \
		-DBUILD_SHARED_LIBS=OFF -Dstatic_runtime=ON -Ddeprecated-functions=OFF -Dencryption=ON -Di2p=OFF \
		-DBOOST_ROOT=/work/build/boost_$VER_BOOST -DBoost_NO_SYSTEM_PATHS=ON -DBoost_NO_BOOST_CMAKE=ON \
		-DOPENSSL_ROOT_DIR="$P" -DOPENSSL_USE_STATIC_LIBS=TRUE >/dev/null
	ninja -j"$JOBS"
	ninja install >/dev/null
fi

# dc-bt
rm -rf "$B/dcbt"
mkdir -p "$B/dcbt"
cd "$B/dcbt"
cmake -G Ninja /work/dcbt \
	-DCMAKE_BUILD_TYPE=Release \
	-DCMAKE_SYSTEM_NAME=Linux -DCMAKE_SYSTEM_PROCESSOR=$CPU \
	-DCMAKE_CXX_COMPILER="$CXX" -DCMAKE_CXX_FLAGS="$ARCHFLAGS" \
	-DCMAKE_PREFIX_PATH="$P" -DCMAKE_FIND_ROOT_PATH="$P" -DCMAKE_FIND_ROOT_PATH_MODE_PACKAGE=BOTH -DCMAKE_FIND_ROOT_PATH_MODE_INCLUDE=BOTH -DCMAKE_FIND_ROOT_PATH_MODE_LIBRARY=BOTH \
	-DBOOST_ROOT=/work/build/boost_$VER_BOOST -DBoost_NO_SYSTEM_PATHS=ON -DBoost_NO_BOOST_CMAKE=ON \
	-DOPENSSL_ROOT_DIR="$P" -DOPENSSL_USE_STATIC_LIBS=TRUE \
	-DJSON_INCLUDE_DIR=/work/src >/dev/null
ninja
cp -f dc-bt /work/out/dc-bt-$T
echo "built /work/out/dc-bt-$T"
