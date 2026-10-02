// Package release holds the rules of a Download Center release: the package
// file names, the SHA256SUMS file and its ed25519 signature, release notes
// from CHANGELOG.md and version ordering. The release tool (cmd/dcrelease) and
// the GitHub workflows use it, so a release is checked the same way where it
// is made and where it is published.
package release

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Arches are the package architectures every release carries.
var Arches = []string{"x86_64", "arm_64", "arm-x41", "arm-x31"}

const (
	SumsName = "SHA256SUMS"
	SigName  = "SHA256SUMS.sig"
	InfoName = "release.json" // listed in SHA256SUMS, so it is signed too
)

// Info is release.json: facts about a release that clients need before they
// install it. Schema is the database layout (store.SchemaVersion) of that
// build; releases without the file have schema 1.
type Info struct {
	Version string `json:"version"`
	Schema  int    `json:"schema"`
}

// AssetName is the file name qbuild gives the package of one architecture.
func AssetName(version, arch string) string {
	return "DownloadCenter_" + version + "_" + arch + ".qpkg"
}

var versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

// ValidVersion accepts 1.2.3 and pre-releases such as 1.2.3-beta.1.
func ValidVersion(v string) bool { return versionRe.MatchString(v) }

// Prerelease reports whether a version is a pre-release (it has a suffix).
func Prerelease(v string) bool { return strings.Contains(v, "-") }

// Compare orders versions: -1 when a < b, 0 when equal, 1 when a > b. A
// pre-release sorts before its release (1.0.0-beta.2 < 1.0.0); pre-release
// identifiers compare numerically when both are numbers, else as text.
func Compare(a, b string) int {
	ac, ap, _ := strings.Cut(a, "-")
	bc, bp, _ := strings.Cut(b, "-")
	an, bn := strings.Split(ac, "."), strings.Split(bc, ".")
	for i := 0; i < 3; i++ {
		if c := cmpIdent(at(an, i), at(bn, i)); c != 0 {
			return c
		}
	}
	switch {
	case ap == "" && bp == "":
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	}
	ai, bi := strings.Split(ap, "."), strings.Split(bp, ".")
	for i := 0; i < len(ai) || i < len(bi); i++ {
		if i >= len(ai) {
			return -1
		}
		if i >= len(bi) {
			return 1
		}
		if c := cmpIdent(ai[i], bi[i]); c != 0 {
			return c
		}
	}
	return 0
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "0"
}

func cmpIdent(a, b string) int {
	x, errA := strconv.Atoi(a)
	y, errB := strconv.Atoi(b)
	switch {
	case errA == nil && errB == nil:
		return cmpInt(x, y)
	case errA == nil:
		return -1 // numeric identifiers sort before text ones
	case errB == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func cmpInt(x, y int) int {
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

// --- keys ---

const (
	pubTag  = "dcrelease-ed25519"
	privTag = "dcrelease-ed25519-private"
)

// GenerateKey makes a new signing key pair in the file formats below.
func GenerateKey() (pubFile, privFile []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return []byte(pubTag + " " + base64.StdEncoding.EncodeToString(pub) + "\n"),
		[]byte(privTag + " " + base64.StdEncoding.EncodeToString(priv) + "\n"), nil
}

func parseKey(b []byte, tag string, size int) ([]byte, error) {
	f := strings.Fields(string(b))
	if len(f) != 2 || f[0] != tag {
		return nil, fmt.Errorf("not a %s key", tag)
	}
	k, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil || len(k) != size {
		return nil, fmt.Errorf("damaged %s key", tag)
	}
	return k, nil
}

// ParsePublic reads a public key file ("dcrelease-ed25519 <base64>").
func ParsePublic(b []byte) (ed25519.PublicKey, error) {
	k, err := parseKey(b, pubTag, ed25519.PublicKeySize)
	return ed25519.PublicKey(k), err
}

// ParsePrivate reads a private key file.
func ParsePrivate(b []byte) (ed25519.PrivateKey, error) {
	k, err := parseKey(b, privTag, ed25519.PrivateKeySize)
	return ed25519.PrivateKey(k), err
}

// KeyID names a public key (first 8 bytes of its SHA-256, hex), so a
// signature made with another key is reported as such.
func KeyID(pub ed25519.PublicKey) string {
	s := sha256.Sum256(pub)
	return hex.EncodeToString(s[:8])
}

// --- SHA256SUMS and its signature ---

// HashFile returns the SHA-256 (hex) and size of a file.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	return HashReader(f)
}

// HashReader returns the SHA-256 (hex) and length of a stream.
func HashReader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// FormatSums writes the sha256sum format ("<hex>  <name>"), sorted by name.
func FormatSums(sums map[string]string) []byte {
	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	sort.Strings(names)
	var b bytes.Buffer
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", sums[n], n)
	}
	return b.Bytes()
}

// ParseSums reads the sha256sum format.
func ParseSums(b []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || len(f[0]) != 64 {
			return nil, fmt.Errorf("%s: bad line %q", SumsName, line)
		}
		if _, err := hex.DecodeString(f[0]); err != nil {
			return nil, fmt.Errorf("%s: bad hash in %q", SumsName, line)
		}
		out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
	}
	return out, sc.Err()
}

// Sign signs the exact bytes of a SHA256SUMS file: "ed25519 <key id> <base64>".
func Sign(priv ed25519.PrivateKey, sums []byte) []byte {
	pub := priv.Public().(ed25519.PublicKey)
	return []byte("ed25519 " + KeyID(pub) + " " + base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums)) + "\n")
}

// Verify checks a signature file against the SHA256SUMS bytes and a public key.
func Verify(pub ed25519.PublicKey, sums, sig []byte) error {
	f := strings.Fields(string(sig))
	if len(f) != 3 || f[0] != "ed25519" {
		return errors.New(SigName + " is not a dcrelease signature")
	}
	if f[1] != KeyID(pub) {
		return fmt.Errorf("%s was made with key %s, expected %s", SigName, f[1], KeyID(pub))
	}
	s, err := base64.StdEncoding.DecodeString(f[2])
	if err != nil || len(s) != ed25519.SignatureSize {
		return errors.New(SigName + " is damaged")
	}
	if !ed25519.Verify(pub, sums, s) {
		return errors.New(SigName + " does not match " + SumsName)
	}
	return nil
}

// CheckSet verifies a complete release: the signature over SHA256SUMS, one
// package per architecture of this version listed in it, and the hashes of
// the package files (name → SHA-256 hex) matching the list. It returns the
// parsed list.
func CheckSet(version string, pub ed25519.PublicKey, sums, sig []byte, hashes map[string]string) (map[string]string, error) {
	if err := Verify(pub, sums, sig); err != nil {
		return nil, err
	}
	list, err := ParseSums(sums)
	if err != nil {
		return nil, err
	}
	for _, a := range Arches {
		n := AssetName(version, a)
		want, ok := list[n]
		if !ok {
			return nil, fmt.Errorf("%s does not list %s", SumsName, n)
		}
		got, ok := hashes[n]
		if !ok {
			return nil, fmt.Errorf("%s is missing", n)
		}
		if got != want {
			return nil, fmt.Errorf("%s: SHA-256 %s, %s says %s", n, got, SumsName, want)
		}
	}
	if want, ok := list[InfoName]; ok && hashes[InfoName] != want {
		return nil, fmt.Errorf("%s does not match %s", InfoName, SumsName)
	}
	return list, nil
}

// --- release notes ---

// Notes returns the CHANGELOG.md section of a version: the text under a
// "## <version>" heading (the heading may go on with a date, e.g.
// "## 1.0.0 - 2026-10-03") up to the next "## " heading.
func Notes(changelog []byte, version string) (string, error) {
	var out []string
	in := false
	for _, line := range strings.Split(string(changelog), "\n") {
		if strings.HasPrefix(line, "## ") {
			if in {
				break
			}
			h := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			h = strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(h, "["), "]"), "v")
			if h == version || strings.HasPrefix(h, version+" ") || strings.HasPrefix(h, version+"]") {
				in = true
			}
			continue
		}
		if in {
			out = append(out, line)
		}
	}
	if !in {
		return "", fmt.Errorf("CHANGELOG.md has no \"## %s\" section", version)
	}
	text := strings.TrimSpace(strings.Join(out, "\n"))
	if text == "" {
		return "", fmt.Errorf("the CHANGELOG.md section for %s is empty", version)
	}
	return text, nil
}
