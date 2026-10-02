#!/bin/sh
# build.sh - Build the DownloadCenter QPKG on a QNAP NAS
# Usage: sh build.sh [x86_64|arm_64|arm-x41]   (default: every architecture)
#
# Needs a Go toolchain (GO=/path/to/go, or go in PATH), node for the JS
# syntax check (optional) and qbuild. The two engines are prebuilt into
# tools/cache: dc-dl (libcurl) by tools/build-dcdl.sh, dc-bt (libtorrent) by
# tools/build-dcbt.sh; they only need rebuilding when they change.

set -e

export PATH="/usr/local/apache/bin:/usr/local/bin:/usr/bin:/bin:$PATH"

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
cd "$SCRIPT_DIR"

GO="${GO:-$(command -v go || true)}"
VERSION=$(sed -n 's/^QPKG_VER="\(.*\)"/\1/p' qpkg.cfg)
ONLY="$1"

echo "=== DownloadCenter QPKG Build (version $VERSION) ==="
echo ""

echo "Cleaning macOS junk files..."
find . -name '.DS_Store' -delete 2>/dev/null || true
find . -name '._*' -delete 2>/dev/null || true
echo ""

# The package is installed on other NAS units: nothing from this machine.
echo "Checking the package is clean..."
CLEAN_ERR=0
if [ -d shared/data ]; then
	echo "  FAIL: shared/data exists (runtime data from a local test)."
	CLEAN_ERR=1
fi
LEFTOVERS=$(find shared icons \( -name '*.db' -o -name '*.db-*' -o -name '*.secret' -o -name '*.session' \
	-o -name '*.log' -o -name '*.torrent' -o -name '*.fastresume' -o -name '*.bak*' -o -name '*.tmp' \) -print)
if [ -n "$LEFTOVERS" ]; then
	echo "  FAIL: data or secret files:"
	echo "$LEFTOVERS" | sed 's/^/    /'
	CLEAN_ERR=1
fi
LOCAL_PATHS=$(find shared -type f ! -name '*.png' ! -name '*.gif' -print | xargs grep -l -e '/share/CACHEDEV' -e '/mnt/HDA_ROOT' 2>/dev/null || true)
if [ -n "$LOCAL_PATHS" ]; then
	echo "  FAIL: paths of this NAS in:"
	echo "$LOCAL_PATHS" | sed 's/^/    /'
	CLEAN_ERR=1
fi
if [ "$CLEAN_ERR" -eq 1 ]; then
	echo "Package is not clean. Aborting build."
	exit 1
fi
echo "  OK"
echo ""

echo "Checking shell syntax..."
for f in shared/DownloadCenter.sh package_routines; do
	sh -n "$f"
	echo "  OK: $f"
done
echo ""

# The license and the third-party notices go into the package (the binaries carry
# statically linked libraries whose licenses require their notices); the UI links
# them, and the integration guide, from the settings
for f in LICENSE THIRD-PARTY-NOTICES.txt docs/INTEGRATION.md; do
	[ -f "$f" ] || { echo "Missing $f. Aborting build."; exit 1; }
done
mkdir -p shared/web/docs
cp -f LICENSE THIRD-PARTY-NOTICES.txt shared/
cp -f LICENSE shared/web/docs/license.txt
cp -f THIRD-PARTY-NOTICES.txt shared/web/docs/third-party-notices.txt
cp -f docs/INTEGRATION.md shared/web/docs/integration.txt

if [ -n "$GO" ]; then
	echo "Generating UI dictionaries..."
	(cd daemon && $GO run ./cmd/mklang js ..)
	echo ""
fi

if command -v node >/dev/null 2>&1; then
	echo "Checking JavaScript syntax..."
	for f in shared/web/js/*.js shared/web/js/lang/*.js; do
		[ -f "$f" ] || continue
		node --check "$f"
		echo "  OK: $f"
	done
	echo ""
fi

if [ -z "$GO" ]; then
	echo "No Go toolchain (set GO=/path/to/go). Aborting build."
	exit 1
fi
echo "Building dcd with $($GO version)..."
(cd daemon && $GO vet ./...)
build_dcd() {
	dir="$1"; goarch="$2"; goarm="$3"
	if [ -n "$ONLY" ] && [ "$ONLY" != "$dir" ]; then
		return 0
	fi
	mkdir -p "$dir/bin"
	# aria2c was bundled up to 0.9.0 (GPL); it must not end up in a package
	rm -f "$dir/bin/aria2c"
	(cd daemon && CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" GOARM="$goarm" \
		$GO build -trimpath -ldflags "-s -w -X main.Version=$VERSION" -o "../$dir/bin/dcd" ./cmd/dcd)
	# The updater picks the package of this architecture (arm-x41 and arm-x31 share one dcd)
	echo "$dir" > "$dir/bin/arch"
	echo "  OK: $dir/bin/dcd"
}
build_dcd x86_64 amd64 ""
build_dcd arm_64 arm64 ""
build_dcd arm-x41 arm 7
if [ -z "$ONLY" ] || [ "$ONLY" = "arm-x41" ]; then
	mkdir -p arm-x31/bin
	rm -f arm-x31/bin/aria2c
	cp -f arm-x41/bin/dcd arm-x31/bin/dcd
	echo arm-x31 > arm-x31/bin/arch
	echo "  OK: arm-x31/bin/dcd (same as arm-x41)"
fi
# dc-dl (libcurl): without it the package cannot download any URL
for d in x86_64 arm_64 arm-x41; do
	if [ -n "$ONLY" ] && [ "$ONLY" != "$d" ] && ! { [ "$ONLY" = arm-x31 ] && [ "$d" = arm-x41 ]; }; then
		continue
	fi
	if [ ! -f "tools/cache/dc-dl-$d" ]; then
		echo "  FAIL: tools/cache/dc-dl-$d is missing (sh tools/build-dcdl.sh $d)"
		exit 1
	fi
	cp -f "tools/cache/dc-dl-$d" "$d/bin/dc-dl"
	chmod 755 "$d/bin/dc-dl"
	echo "  OK: $d/bin/dc-dl"
done
[ -f tools/cache/dc-dl-arm-x41 ] && cp -f tools/cache/dc-dl-arm-x41 arm-x31/bin/dc-dl
# dc-bt (libtorrent), when built by tools/build-dcbt.sh
for d in x86_64 arm_64 arm-x41; do
	if [ -f "tools/cache/dc-bt-$d" ]; then
		cp -f "tools/cache/dc-bt-$d" "$d/bin/dc-bt"
		chmod 755 "$d/bin/dc-bt"
		echo "  OK: $d/bin/dc-bt"
	fi
done
[ -f tools/cache/dc-bt-arm-x41 ] && cp -f tools/cache/dc-bt-arm-x41 arm-x31/bin/dc-bt
echo ""

echo "Cleaning previous build output..."
rm -f build/*.qpkg build/*.md5 2>/dev/null
echo ""

echo "Running qbuild..."
if [ -n "$ONLY" ]; then
	qbuild --build-arch "$ONLY"
else
	qbuild
fi
echo ""

echo "=== Build Results ==="
if ls build/*.qpkg 1>/dev/null 2>&1; then
	ls -lh build/*.qpkg
	echo ""
	echo "Build successful!"
else
	echo "No .qpkg file found in build/. Build may have failed."
	exit 1
fi
