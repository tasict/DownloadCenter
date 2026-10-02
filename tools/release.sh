#!/bin/sh
#
# release.sh - build, sign and hand a release to GitHub.
#
# Usage (on the build NAS, from anywhere):  sh tools/release.sh <version>
#
# 1. checks: the version matches qpkg.cfg and has a CHANGELOG.md section, the
#    working tree is clean, master is pushed, the tag is new;
# 2. go vet and tests, then build.sh for every architecture;
# 3. SHA256SUMS of the four packages, signed with the release key;
# 4. a draft GitHub Release with the packages, SHA256SUMS and its signature;
# 5. the tag v<version>, pushed. Its push starts .github/workflows/release.yml,
#    which checks the draft again and publishes it.
#
# Local settings (never committed) go in .secret/: github_token (a
# fine-grained token for this repository with Contents read/write),
# release.key (made once with: sh tools/release.sh --keygen) and, optionally,
# release.env (sourced first; set GO, GIT, PATH or Go cache variables there).
#

set -eu

REPO="tasict/DownloadCenter"
BRANCH="master"

cd "$(dirname "$0")/.."
ROOT=$(pwd)
SECRET="$ROOT/.secret"
TOKEN_FILE="$SECRET/github_token"
KEY_FILE="$SECRET/release.key"
PUB_FILE="$ROOT/tools/release-key.pub"
URL="https://github.com/$REPO.git"

die() { echo "release: $*" >&2; exit 1; }
step() { echo ""; echo "== $*"; }

[ -f "$SECRET/release.env" ] && . "$SECRET/release.env"
GO="${GO:-$(command -v go || true)}"
GIT="${GIT:-$(command -v git || true)}"
[ -n "$GO" ] && [ -x "$GO" ] || die "no Go toolchain (set GO in .secret/release.env)"

dcrelease() { (cd "$ROOT/daemon" && "$GO" run ./cmd/dcrelease "$@"); }

if [ "${1:-}" = "--keygen" ]; then
	mkdir -p "$SECRET" && chmod 700 "$SECRET"
	dcrelease keygen -key "$KEY_FILE" -pub "$PUB_FILE"
	echo "Commit tools/release-key.pub; keep $KEY_FILE (and a backup of it) private."
	exit 0
fi

V="${1:-}"
[ -n "$V" ] || die "usage: sh tools/release.sh <version> | --keygen"
echo "$V" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$' || die "version must look like 1.2.3 or 1.2.3-beta.1"
TAG="v$V"

step "Checks"
[ -n "$GIT" ] && [ -x "$GIT" ] || die "no git (set GIT in .secret/release.env)"
CFG=$(sed -n 's/^QPKG_VER="\(.*\)"/\1/p' qpkg.cfg)
[ "$CFG" = "$V" ] || die "qpkg.cfg has QPKG_VER=\"$CFG\"; set it to \"$V\", commit and push first"
[ -f "$TOKEN_FILE" ] || die "missing $TOKEN_FILE"
[ -f "$KEY_FILE" ] || die "missing $KEY_FILE (sh tools/release.sh --keygen)"
[ -f "$PUB_FILE" ] || die "missing tools/release-key.pub"
dcrelease notes -version "$V" -changelog "$ROOT/CHANGELOG.md" >/dev/null

[ "$("$GIT" rev-parse --abbrev-ref HEAD)" = "$BRANCH" ] || die "not on $BRANCH"
[ -z "$("$GIT" status --porcelain)" ] || die "the working tree has uncommitted changes"

# git asks this helper for credentials: the token never appears in a command line or URL
ASKPASS=$(mktemp)
trap 'rm -f "$ASKPASS"' EXIT INT TERM
cat > "$ASKPASS" <<EOF
#!/bin/sh
case "\$1" in
	Username*) echo x-access-token ;;
	*) tr -d '\r\n' < "$TOKEN_FILE" ;;
esac
EOF
chmod 700 "$ASKPASS"
export GIT_ASKPASS="$ASKPASS" GIT_TERMINAL_PROMPT=0

"$GIT" fetch -q "$URL" "$BRANCH"
HEAD=$("$GIT" rev-parse HEAD)
[ "$HEAD" = "$("$GIT" rev-parse FETCH_HEAD)" ] || die "local $BRANCH is not what GitHub has; push (or pull) first"
[ -z "$("$GIT" tag -l "$TAG")" ] || die "tag $TAG already exists here"
[ -z "$("$GIT" ls-remote --tags "$URL" "refs/tags/$TAG")" ] || die "tag $TAG already exists on GitHub"
echo "version $V, commit $HEAD"

step "Vet and test"
(cd daemon && "$GO" vet ./... && "$GO" test ./...)

step "Build"
GO="$GO" sh build.sh
for a in x86_64 arm_64 arm-x41 arm-x31; do
	[ -f "build/DownloadCenter_${V}_$a.qpkg" ] || die "build/DownloadCenter_${V}_$a.qpkg was not built"
done

step "Sign"
dcrelease sums -version "$V" -dir "$ROOT/build"
dcrelease sign -key "$KEY_FILE" -dir "$ROOT/build"
dcrelease check -version "$V" -dir "$ROOT/build" -pub "$PUB_FILE"

step "Draft release"
dcrelease draft -repo "$REPO" -token-file "$TOKEN_FILE" -version "$V" -commit "$HEAD" -dir "$ROOT/build" -changelog "$ROOT/CHANGELOG.md"

step "Tag"
"$GIT" tag -a "$TAG" -m "Download Center $V" "$HEAD"
"$GIT" push -q "$URL" "refs/tags/$TAG"

echo ""
echo "Done. GitHub Actions now checks the draft and publishes it:"
echo "  https://github.com/$REPO/actions"
