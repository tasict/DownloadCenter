package phpbox

import (
	"archive/tar"
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const testInfo = `{"name":"testsearch","displayname":"Test search","description":"A module for tests","version":"1.0",
	"site":"http://search.test/","module":"search.php","type":"search","class":"DcTestSearch","accountsupport":true}`

const testAddonJSON = `{"name":"Test add-on","author":"Tests","website":"http://search.test/","class":"DcTestAddon",
	"addon":"main.php","hosts":["search.test"],"domain":"search.test","version":123,
	"interface":["ISearch","IVerify","IDownload"],"description":{"ENG":"An add-on for tests","TCH":"測試用"}}`

func pkgErr(t *testing.T, err error, msg string) {
	t.Helper()
	var pe *PackageError
	if !errors.As(err, &pe) || pe.Msg != msg {
		t.Errorf("got %v, want %q", err, msg)
	}
}

func TestInspectDLM(t *testing.T) {
	data := dlmOf(t, []entry{{name: "INFO", body: testInfo}, {name: "./search.php", body: "<?php class DcTestSearch {}"}})
	p, err := Inspect("test.dlm", data)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if p.Format != FormatDLM || p.Name != "testsearch" || p.DisplayName != "Test search" || p.Version != "1.0" ||
		p.Entry != "search.php" || p.Class != "DcTestSearch" || !p.Account || p.SHA256 != hex.EncodeToString(sum[:]) ||
		p.Site != "http://search.test/" || len(p.Files) != 2 {
		t.Errorf("%+v", p)
	}
	module := entry{name: "search.php", body: "<?php"}
	cases := []struct {
		name    string
		file    string
		entries []entry
		msg     string
	}{
		// spec: hosting modules are refused
		{"host file", "x.host", []entry{{name: "INFO", body: testInfo}, module}, MsgOnlySearch},
		{"host type", "x.dlm", []entry{{name: "INFO", body: strings.Replace(testInfo, `"search"`, `"host"`, 1)}, module}, MsgOnlySearch},
		// spec: a link to /etc/config is refused
		{"symlink", "x.dlm", []entry{{name: "INFO", body: testInfo}, module, {name: "cfg", typ: tar.TypeSymlink, link: "/etc/config"}}, MsgUnsafe},
		{"hard link", "x.dlm", []entry{{name: "INFO", body: testInfo}, module, {name: "h", typ: tar.TypeLink, link: "search.php"}}, MsgUnsafe},
		{"device", "x.dlm", []entry{{name: "INFO", body: testInfo}, module, {name: "d", typ: tar.TypeChar}}, MsgUnsafe},
		{"fifo", "x.dlm", []entry{{name: "INFO", body: testInfo}, module, {name: "f", typ: tar.TypeFifo}}, MsgUnsafe},
		{"parent", "x.dlm", []entry{{name: "INFO", body: testInfo}, module, {name: "../evil.php", body: "x"}}, MsgUnsafe},
		{"nested parent", "x.dlm", []entry{{name: "INFO", body: testInfo}, module, {name: "a/../../evil", body: "x"}}, MsgUnsafe},
		{"absolute", "x.dlm", []entry{{name: "INFO", body: testInfo}, module, {name: "/etc/cron.d/x", body: "x"}}, MsgUnsafe},
		{"twice", "x.dlm", []entry{{name: "INFO", body: testInfo}, module, {name: "./search.php", body: "y"}}, MsgUnsafe},
		{"no INFO", "x.dlm", []entry{module}, MsgMissing},
		{"no module", "x.dlm", []entry{{name: "INFO", body: testInfo}}, MsgMissing},
		{"bad class", "x.dlm", []entry{{name: "INFO", body: strings.Replace(testInfo, "DcTestSearch", "a b", 1)}, module}, MsgField},
		{"bad INFO", "x.dlm", []entry{{name: "INFO", body: "{"}, module}, MsgField},
	}
	for _, c := range cases {
		_, err := Inspect(c.file, dlmOf(t, c.entries))
		pkgErr(t, err, c.msg)
	}
	// Limits: files, unpacked bytes, the file itself
	var many []entry
	for i := 0; i < MaxEntries+1; i++ {
		many = append(many, entry{name: "f" + strconv.Itoa(i), body: "x"})
	}
	_, err = Inspect("x.dlm", dlmOf(t, append([]entry{{name: "INFO", body: testInfo}, module}, many...)))
	pkgErr(t, err, MsgEntries)
	_, err = Inspect("x.dlm", dlmOf(t, []entry{{name: "INFO", body: testInfo}, module, {name: "zeros", body: strings.Repeat("\x00", MaxUnpacked)}}))
	pkgErr(t, err, MsgUnpacked)
	_, err = Inspect("x.dlm", make([]byte, MaxPackage+1))
	pkgErr(t, err, MsgTooLarge)
	_, err = Inspect("x.dlm", []byte("not a package"))
	pkgErr(t, err, MsgNotPlugin)
}

func TestInspectAddon(t *testing.T) {
	base := []entry{{name: "addon.json", body: testAddonJSON}, {name: "main.php", body: "<?php class DcTestAddon {}"}}
	for _, nl := range []bool{true, false} {
		data := addonOf(t, base, nl)
		p, err := Inspect("test.addon", data)
		if err != nil {
			t.Fatalf("newline %v: %v", nl, err)
		}
		if p.Format != FormatAddon || p.Name != "Test add-on" || p.Version != "1.23" || p.Entry != "main.php" || p.Class != "DcTestAddon" ||
			!p.Account || p.Description != "An add-on for tests" || p.Descriptions["TCH"] != "測試用" || len(p.KeyFingerprint) != 64 ||
			strings.Join(p.Interfaces, ",") != "ISearch,IVerify,IDownload" || p.Domain != "search.test" {
			t.Errorf("%+v", p)
		}
	}
	// spec: a changed add-on is refused
	data := addonOf(t, base, true)
	tampered := append([]byte(nil), data...)
	i := bytes.Index(tampered, []byte("DcTestAddon {}"))
	tampered[i] = 'X'
	_, err := Inspect("x.addon", tampered)
	pkgErr(t, err, MsgDamaged)
	// Signed by another key than the one inside
	other, _ := rsa.GenerateKey(rand.Reader, 1024)
	resigned := append([]byte(nil), data[:len(data)-sigSize]...)
	sig, _ := rsa.SignPKCS1v15(nil, other, crypto.Hash(0), []byte("0000"))
	_, err = Inspect("x.addon", append(resigned, sig...))
	pkgErr(t, err, MsgDamaged)

	cases := []struct {
		name string
		json string
		msg  string
	}{
		{"no search", strings.Replace(testAddonJSON, `"ISearch",`, "", 1), MsgNoSearch},
		{"unknown interface", strings.Replace(testAddonJSON, `"IDownload"`, `"IShell"`, 1), MsgField},
		{"no English", strings.Replace(testAddonJSON, `"ENG"`, `"GER"`, 1), MsgField},
		{"low version", strings.Replace(testAddonJSON, `123`, `99`, 1), MsgField},
		{"no entry", strings.Replace(testAddonJSON, `"main.php"`, `"other.php"`, 1), MsgMissing},
		{"entry outside", strings.Replace(testAddonJSON, `"main.php"`, `"../main.php"`, 1), MsgField},
	}
	for _, c := range cases {
		_, err := Inspect("x.addon", addonOf(t, []entry{{name: "addon.json", body: c.json}, base[1]}, true))
		if err == nil {
			t.Errorf("%s: accepted", c.name)
			continue
		}
		pkgErr(t, err, c.msg)
	}
	// Keys: RSA of 1024 bits only
	k2048, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKIXPublicKey(&k2048.PublicKey)
	if _, _, err := addonKey(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})); err == nil {
		t.Error("2048-bit key accepted")
	}
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ = x509.MarshalPKIXPublicKey(&ec.PublicKey)
	if _, _, err := addonKey(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})); err == nil {
		t.Error("EC key accepted")
	}
	k := addonKeyPair(t)
	if _, fp, err := addonKey(pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&k.PublicKey)})); err != nil || len(fp) != 64 {
		t.Errorf("PKCS #1 key: %v", err)
	}
}

// crypto/rsa must keep verifying 1024-bit signatures: add-ons are signed
// with such keys.
func TestRSA1024Verifies(t *testing.T) {
	k := addonKeyPair(t)
	msg := []byte(strings.Repeat("ab", 20) + "\n")
	sig, err := rsa.SignPKCS1v15(nil, k, crypto.Hash(0), msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(&k.PublicKey, crypto.Hash(0), msg, sig); err != nil {
		t.Fatalf("this Go refuses 1024-bit RSA: %v", err)
	}
}

func TestExtract(t *testing.T) {
	p, err := Inspect("t.dlm", dlmOf(t, []entry{{name: "INFO", body: testInfo}, {name: "search.php", body: "<?php"}, {name: "lib/", typ: tar.TypeDir}, {name: "lib/x.php", body: "<?php"}}))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "code")
	if err := p.Extract(dir); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{".": 0o700, "lib": 0o700, "INFO": 0o600, "lib/x.php": 0o600} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || st.Mode().Perm() != want {
			t.Errorf("%s: %v %v", name, st.Mode(), err)
		}
	}
	if err := p.Extract(dir); err == nil {
		t.Error("extracted over an existing directory")
	}
	if pl := p.Plugin(dir); pl.Format != FormatDLM || pl.Entry != "search.php" || pl.Class != "DcTestSearch" || !pl.Account || pl.Dir != dir {
		t.Errorf("%+v", pl)
	}
}
