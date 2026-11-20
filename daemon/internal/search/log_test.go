package search

import (
	"bytes"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"

	"downloadcenter/internal/phpbox"
)

// spec: the log gets the source id and our fixed reason, never text a
// source or a plugin wrote (it can carry the keyword in any encoding), and
// never the account's user name
func TestLogHasNoSourceText(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	e := newAPIEnv(t)
	box := okBox()
	e.s.code = box
	data := codeDLM("1.0")
	e.call(t, "admin", "", "POST", "/search/plugins", with(upload(data, "t.dlm"), "sha256", sum(data), "accept_risk", true))
	on := true
	e.s.store.Update("dlm-testsearch", &on, map[string]string{"username": "alicia", "password": "pw"}, nil)
	// "ubuntu" in base64, reversed and spelt out, the user name as it is
	const leak = "dWJ1bnR1 utnubu u-b-u-n-t-u alicia SOURCETEXT"
	box.err, box.errText = phpbox.ErrFailed, "the plugin failed: "+leak
	e.n.handle("err.example.org", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<error code="201" description="` + leak + `"/>`))
	})
	e.s.store.Install(&Plugin{Schema: 1, ID: "err", Name: "Err", Type: TypeTorznab}, true, false,
		map[string]string{"url": "http://err.example.org/api", "apikey": "K"})
	id, err := e.s.Start("admin", true, Query{Text: "ubuntu"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v := waitDone(t, e.s, "admin", id)
	if len(v.Sources) != 2 {
		t.Fatalf("%+v", v.Sources)
	}
	for _, bad := range strings.Fields(leak) {
		if strings.Contains(buf.String(), bad) {
			t.Errorf("%q in the log:\n%s", bad, buf.String())
		}
	}
	for _, want := range []string{"source err failed: " + ReasonTorznab, "source dlm-testsearch failed: " + ReasonPluginFailed} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("no %q in:\n%s", want, buf.String())
		}
	}
}
