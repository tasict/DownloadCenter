package torrent

import (
	"strings"
	"testing"
)

func TestParseMultiFile(t *testing.T) {
	info := "d5:filesld6:lengthi10e4:pathl1:a5:x.binee" +
		"d4:attr1:p6:lengthi6e4:pathl4:.pad1:0eed6:lengthi20e4:pathl5:y.txteee" +
		"4:name4:demo12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaae"
	b := []byte("d8:announce14:udp://t.org:1213:announce-listll14:udp://t.org:12el11:http://t2/aee7:comment2:hi4:info" + info + "e")
	m, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "demo" || !m.IsFolder || len(m.Files) != 3 || m.Size != 30 {
		t.Fatalf("got %+v", m)
	}
	if !m.Files[1].Pad || m.Files[2].Index != 2 || m.Files[0].Path != "demo/a/x.bin" {
		t.Errorf("files %+v", m.Files)
	}
	if len(m.Trackers) != 2 || m.Comment != "hi" || len(m.InfoHash) != 40 {
		t.Errorf("meta %+v", m)
	}
	if _, err := Parse([]byte("d4:infoi1ee")); err == nil {
		t.Error("invalid torrent accepted")
	}
}

func TestParseMagnet(t *testing.T) {
	m, err := ParseMagnet("magnet:?xt=urn:btih:08ADA5A7A6183AAE1E09D831DF6748D566095A10&dn=Sintel&tr=udp%3A%2F%2Fa%3A1")
	if err != nil || m.InfoHash != "08ada5a7a6183aae1e09d831df6748d566095a10" || m.Name != "Sintel" || len(m.Trackers) != 1 {
		t.Fatalf("got %+v %v", m, err)
	}
	b32, err := ParseMagnet("magnet:?xt=urn:btih:BCW2LJ5GDA5K4HQJ3AY56Z2I2VTAJVAQ")
	if err != nil || len(b32.InfoHash) != 40 {
		t.Fatalf("base32: %+v %v", b32, err)
	}
	if _, err := ParseMagnet("magnet:?dn=x"); err == nil {
		t.Error("magnet without hash accepted")
	}
	if !strings.HasPrefix(MagnetURI("ab", "n", []string{"t"}), "magnet:?xt=urn:btih:ab&") {
		t.Error("MagnetURI")
	}
}
