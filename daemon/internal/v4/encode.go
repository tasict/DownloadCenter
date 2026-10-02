package v4

import (
	"encoding/base64"
	"strings"
	"unicode/utf8"
)

const b64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// ezEncode is the official clients' ezEncode: Base64 of the UTF-8 bytes.
func ezEncode(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// ezDecode mirrors the client's ezDecode: characters outside the alphabet
// are skipped and decoding stops at '='.
func ezDecode(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c == '=' {
			break
		}
		if strings.ContainsRune(b64Alphabet, c) {
			b.WriteRune(c)
		}
	}
	clean := b.String()
	if len(clean)%4 == 1 {
		clean = clean[:len(clean)-1]
	}
	out, err := base64.RawStdEncoding.DecodeString(clean)
	if err != nil {
		return ""
	}
	return string(out)
}

// fixDoubleUTF8 undoes the official UI's utf16to8-then-encode quirk: a string
// whose runes are all U+0000..U+00FF and whose bytes form valid UTF-8 is the
// UTF-8 encoding applied twice. Plain UTF-8 from other clients is kept.
func fixDoubleUTF8(s string) string {
	if !utf8.ValidString(s) {
		return s
	}
	high := false
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xFF {
			return s
		}
		if r >= 0x80 {
			high = true
		}
		b = append(b, byte(r))
	}
	if !high || !utf8.Valid(b) {
		return s
	}
	return string(b)
}
