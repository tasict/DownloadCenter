#!/bin/sh
# build-dcdl.sh - build dc-dl (libcurl transfer engine) for the QDK
# architectures as static musl binaries, in a Docker container.
#
# Usage: sh tools/build-dcdl.sh [x86_64|arm_64|arm-x41 ...]   (default: all)
# Output: tools/cache/dc-dl-<arch>; build.sh copies them into <arch>/bin/.
#
# Needs Docker (Container Station). Only needed when a library or dc-dl
# itself changes; dcd is built with plain `go build`. Everything downloaded
# is pinned by SHA-256 below (taken from the first download; a mismatch
# aborts). Pieces:
#   - curl 8.22.0 (libcurl, static): http, https, ftp, ftps, sftp, scp only
#   - nghttp2 1.70.0 (HTTP/2), libssh2 1.11.1 (SFTP/SCP), zlib 1.3.2
#   - OpenSSL 3.5.9 and the musl toolchains are shared with dc-bt
#     (tools/build-dcbt.sh pins them; the build tree under
#     tools/cache/dcbt-build/<arch>/prefix is reused)
#   - The QTS 5.10 kernel answers fchmodat2 with EFAULT; the container runs
#     with tools/dcbt/seccomp.json mapping it to ENOSYS (see build-dcbt.sh).

set -e
cd "$(dirname "$(readlink -f "$0")")/.."
ROOT=$(pwd)
SRC=tools/cache/src
mkdir -p "$SRC" tools/cache/dcbt-build

CS=$(/sbin/getcfg container-station Install_Path -f /etc/config/qpkg.conf 2>/dev/null || true)
DOCKER=${DOCKER:-$CS/bin/docker}
[ -x "$DOCKER" ] || DOCKER=$(command -v docker || true)
[ -n "$DOCKER" ] || { echo "docker not found"; exit 1; }
export DOCKER_HOST=${DOCKER_HOST:-unix:///var/run/docker.sock}

# <file> <sha256> <url>
sources() {
	cat <<LIST
curl-8.22.0.tar.xz f7ef3ae8a22e521f289803fe93543eb64c329b58aa73a9e224dfd915a2a5f4f7 https://curl.se/download/curl-8.22.0.tar.xz
nghttp2-1.70.0.tar.xz e05cb1388eaca3830aded4ccf20044b6e1ac1a61411dcca11b0437c4285c8bc2 https://github.com/nghttp2/nghttp2/releases/download/v1.70.0/nghttp2-1.70.0.tar.xz
libssh2-1.11.1.tar.xz 9954cb54c4f548198a7cbebad248bdc87dd64bd26185708a294b2b50771e3769 https://libssh2.org/download/libssh2-1.11.1.tar.xz
zlib-1.3.2.tar.xz d7a0654783a4da529d1bb793b7ad9c3318020af77667bcae35f95d0e42a792f3 https://zlib.net/zlib-1.3.2.tar.xz
openssl-3.5.9.tar.gz 603f5602e2eef00d77fbd429d34dcd5822bb301757a1bc9cdb24c670f1eb859a https://www.openssl.org/source/openssl-3.5.9.tar.gz
json.hpp aaf127c04cb31c406e5b04a63f1ae89369fccde6d8fa7cdda1ed4f32dfc5de63 https://raw.githubusercontent.com/nlohmann/json/v3.12.0/single_include/nlohmann/json.hpp
aarch64-linux-musl-cross.tgz c909817856d6ceda86aa510894fa3527eac7989f0ef6e87b5721c58737a06c38 https://musl.cc/aarch64-linux-musl-cross.tgz
armv7l-linux-musleabihf-cross.tgz f49f1a15ec62364ef5e4edb4e3990c0e1d2d1a54c90153b8f3869dad63328a10 https://musl.cc/armv7l-linux-musleabihf-cross.tgz
LIST
}

echo "Fetching pinned sources..."
sources | while read -r f sum url; do
	if [ ! -f "$SRC/$f" ]; then
		echo "  downloading $f"
		curl -fsSL -o "$SRC/$f.part" "$url"
		mv "$SRC/$f.part" "$SRC/$f"
	fi
	got=$(sha256sum "$SRC/$f" | cut -d' ' -f1)
	if [ "$got" != "$sum" ]; then
		echo "SHA-256 mismatch for $f: $got (expected $sum)"
		exit 1
	fi
	echo "  OK: $f"
done

if ! "$DOCKER" image inspect dcdl-builder:1 >/dev/null 2>&1; then
	echo "Building the dcdl-builder image..."
	"$DOCKER" build -t dcdl-builder:1 tools/dcdl
fi

TARGETS="$*"
[ -n "$TARGETS" ] || TARGETS="x86_64 arm_64 arm-x41"
for t in $TARGETS; do
	echo "Building dc-dl for $t..."
	"$DOCKER" rm -f "dcdl-build-$t" >/dev/null 2>&1 || true
	"$DOCKER" run --rm --name "dcdl-build-$t" --security-opt "seccomp=$ROOT/tools/dcbt/seccomp.json" \
		-v "$ROOT/$SRC:/work/src:ro" \
		-v "$ROOT/dcdl:/work/dcdl:ro" \
		-v "$ROOT/tools/dcdl:/work/tools:ro" \
		-v "$ROOT/tools/cache/dcbt-build:/work/build" \
		-v "$ROOT/tools/cache:/work/out" \
		dcdl-builder:1 sh /work/tools/build-in-container.sh "$t"
	ls -l "tools/cache/dc-dl-$t"
done
