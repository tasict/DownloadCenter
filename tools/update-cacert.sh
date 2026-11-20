#!/bin/sh
# update-cacert.sh - refresh the list of trusted certificate authorities that
# ships with Download Center (shared/certs/cacert.pem, installed as
# <QPKG_ROOT>/certs/cacert.pem). Old firmware keeps an outdated system store;
# dcd, dc-bt and dc-dl trust this list together with the system's.
#
# Usage: sh tools/update-cacert.sh      (run before every release)
#
# The list is curl.se's extract of Mozilla's root store (certdata.txt). It
# must match the SHA-256 curl.se publishes beside it, hold PEM certificates,
# and still come from the source and licence recorded in
# THIRD-PARTY-NOTICES.txt; otherwise nothing is replaced.

set -e
cd "$(dirname "$(readlink -f "$0")")/.."
URL=https://curl.se/ca/cacert.pem
OUT=shared/certs/cacert.pem

tmp=$(mktemp -d "${TMPDIR:-/tmp}/cacert.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM

curl -fsSL -o "$tmp/cacert.pem" "$URL"
curl -fsSL -o "$tmp/cacert.pem.sha256" "$URL.sha256"
want=$(cut -d' ' -f1 <"$tmp/cacert.pem.sha256")
got=$(sha256sum "$tmp/cacert.pem" | cut -d' ' -f1)
if [ -z "$want" ] || [ "$got" != "$want" ]; then
	echo "SHA-256 mismatch: $got (curl.se publishes $want)"
	exit 1
fi

# The header is the lines starting with ##
sed -n '/^##/p;/^-----BEGIN/q' "$tmp/cacert.pem" >"$tmp/header"
cat "$tmp/header"
if ! grep -q "certdata.txt" "$tmp/header"; then
	echo "The header no longer names Mozilla's certdata.txt as its source"
	exit 1
fi
# A licence named in the header must be the one in THIRD-PARTY-NOTICES.txt
if grep -qi "licen[cs]e" "$tmp/header" && ! grep -qi "Mozilla Public License.*2\.0\|MPL[- ]2\.0\|MPL 2" "$tmp/header"; then
	echo "The header names another licence than the Mozilla Public License 2.0"
	exit 1
fi
if ! grep -q "Mozilla Public License Version 2.0" THIRD-PARTY-NOTICES.txt; then
	echo "THIRD-PARTY-NOTICES.txt lacks the Mozilla Public License 2.0"
	exit 1
fi

n=$(grep -c "BEGIN CERTIFICATE" "$tmp/cacert.pem")
if [ "$n" -lt 100 ]; then
	echo "Only $n certificates in the download"
	exit 1
fi
mkdir -p "$(dirname "$OUT")"
cp "$tmp/cacert.pem" "$OUT.tmp"
chmod 644 "$OUT.tmp"
mv "$OUT.tmp" "$OUT"
echo "$OUT: $n certificates, SHA-256 $got"
