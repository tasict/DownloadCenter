package release

import (
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	order := []string{"0.9.1", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-beta.2", "1.0.0-beta.10", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.10.0", "2.0.0"}
	for i := range order {
		for j := range order {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := Compare(order[i], order[j]); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
}

func TestValidVersion(t *testing.T) {
	for v, ok := range map[string]bool{"1.0.0": true, "1.0.0-beta.1": true, "v1.0.0": false, "1.0": false, "1.0.0-": false, "1.0.0 ": false} {
		if ValidVersion(v) != ok {
			t.Errorf("ValidVersion(%q) != %v", v, ok)
		}
	}
}

func TestSignAndCheck(t *testing.T) {
	pubFile, privFile, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ParsePublic(pubFile)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := ParsePrivate(privFile)
	if err != nil {
		t.Fatal(err)
	}
	hashes := map[string]string{}
	for i, a := range Arches {
		hashes[AssetName("1.0.0", a)] = strings.Repeat(string("abcd"[i]), 64)
	}
	sums := FormatSums(hashes)
	sig := Sign(priv, sums)
	if _, err := CheckSet("1.0.0", pub, sums, sig, hashes); err != nil {
		t.Fatalf("good release rejected: %v", err)
	}
	// a changed package
	bad := map[string]string{}
	for k, v := range hashes {
		bad[k] = v
	}
	bad[AssetName("1.0.0", "x86_64")] = strings.Repeat("e", 64)
	if _, err := CheckSet("1.0.0", pub, sums, sig, bad); err == nil {
		t.Error("changed package accepted")
	}
	// a changed list
	if _, err := CheckSet("1.0.0", pub, append([]byte{}, append(sums, '\n')...), sig, hashes); err == nil {
		t.Error("changed SHA256SUMS accepted")
	}
	// another key
	other, _, _ := GenerateKey()
	opub, _ := ParsePublic(other)
	if err := Verify(opub, sums, sig); err == nil || !strings.Contains(err.Error(), "key") {
		t.Errorf("signature of another key: %v", err)
	}
	// a missing architecture
	delete(hashes, AssetName("1.0.0", "arm-x31"))
	short := FormatSums(hashes)
	if _, err := CheckSet("1.0.0", pub, short, Sign(priv, short), hashes); err == nil {
		t.Error("release without arm-x31 accepted")
	}
}

func TestNotes(t *testing.T) {
	cl := []byte("# Changelog\n\nIntro.\n\n## 1.1.0-beta.1 - 2026-11-01\n\n- Beta.\n\n## 1.0.0 - 2026-10-03\n\n### Added\n- One.\n- Two.\n\n## 0.9.1\n\n- Old.\n")
	n, err := Notes(cl, "1.0.0")
	if err != nil || n != "### Added\n- One.\n- Two." {
		t.Errorf("Notes 1.0.0 = %q, %v", n, err)
	}
	if n, _ := Notes(cl, "0.9.1"); n != "- Old." {
		t.Errorf("Notes 0.9.1 = %q", n)
	}
	if _, err := Notes(cl, "1.0"); err == nil {
		t.Error("prefix version matched")
	}
	if _, err := Notes(cl, "2.0.0"); err == nil {
		t.Error("missing section accepted")
	}
}

func TestParseSums(t *testing.T) {
	h := strings.Repeat("a", 64)
	m, err := ParseSums([]byte(h + "  DownloadCenter_1.0.0_x86_64.qpkg\n" + strings.Repeat("b", 64) + " *x.bin\n"))
	if err != nil || m["DownloadCenter_1.0.0_x86_64.qpkg"] != h || m["x.bin"] == "" {
		t.Errorf("ParseSums = %v, %v", m, err)
	}
	if _, err := ParseSums([]byte("zz  file\n")); err == nil {
		t.Error("bad line accepted")
	}
}
