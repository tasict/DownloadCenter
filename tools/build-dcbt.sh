#!/bin/sh
# build-dcbt.sh - build dc-bt (libtorrent torrent engine) for the QDK
# architectures as static musl binaries, in a Docker container.
#
# Usage: sh tools/build-dcbt.sh [x86_64|arm_64|arm-x41 ...]   (default: all)
# Output: tools/cache/dc-bt-<arch>; build.sh copies them into <arch>/bin/.
#
# Needs Docker (Container Station). Everything downloaded is pinned by
# SHA-256 below; a mismatch aborts. Pieces:
#   - libtorrent-rasterbar 2.0.15 (RC_2_0), static, deprecated API off
#   - Boost 1.88.0 headers only (libtorrent 2.0 links no Boost library)
#   - OpenSSL 3.5.9 LTS, static
#   - nlohmann/json 3.12.0 (single header)
#   - x86_64 is built natively with Alpine 3.20's gcc (musl); aarch64 and
#     armv7 (hard float) use the musl.cc cross toolchains (gcc 11, musl),
#     so no qemu/binfmt is needed. QNAP's GitHub downloads are slow, so the
#     large archives come from archives.boost.io, openssl.org and musl.cc.
#   - The QTS 5.10 kernel answers fchmodat2 with EFAULT instead of ENOSYS,
#     which breaks tar/install in current musl and glibc userlands; the
#     container runs with tools/dcbt/seccomp.json mapping it to ENOSYS.

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
libtorrent-rasterbar-2.0.15.tar.gz 5e2e79129823b7ea48721164c32b5aaf83d3fd733b5502100f6705b29f27bb02 https://github.com/arvidn/libtorrent/releases/download/v2.0.15/libtorrent-rasterbar-2.0.15.tar.gz
boost_1_88_0.tar.gz 3621533e820dcab1e8012afd583c0c73cf0f77694952b81352bf38c1488f9cb4 https://archives.boost.io/release/1.88.0/source/boost_1_88_0.tar.gz
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

if ! "$DOCKER" image inspect dcbt-builder:1 >/dev/null 2>&1; then
	echo "Building the dcbt-builder image..."
	"$DOCKER" build -t dcbt-builder:1 tools/dcbt
fi

TARGETS="$*"
[ -n "$TARGETS" ] || TARGETS="x86_64 arm_64 arm-x41"
for t in $TARGETS; do
	echo "Building dc-bt for $t..."
	"$DOCKER" rm -f "dcbt-build-$t" >/dev/null 2>&1 || true
	"$DOCKER" run --rm --name "dcbt-build-$t" --security-opt "seccomp=$ROOT/tools/dcbt/seccomp.json" \
		-v "$ROOT/$SRC:/work/src:ro" \
		-v "$ROOT/dcbt:/work/dcbt:ro" \
		-v "$ROOT/tools/dcbt:/work/tools:ro" \
		-v "$ROOT/tools/cache/dcbt-build:/work/build" \
		-v "$ROOT/tools/cache:/work/out" \
		dcbt-builder:1 sh /work/tools/build-in-container.sh "$t"
	ls -l "tools/cache/dc-bt-$t"
done
