package search

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"downloadcenter/internal/phpbox"
	"downloadcenter/internal/phpbox/phpboxtest"
)

// An add-on that answers from its own files: the sandbox runs for real,
// without the network.
const fileAddon = `<?php
class DcFileSearch implements ISite, ISearch
{
    public function __construct($url = null, $username = null, $password = null, $meta = null)
    {
    }

    public function Search($keyword, $limit, $category)
    {
        $out = array();
        foreach (json_decode(file_get_contents('/plugin/answer.json'), true) as $row) {
            $l = new SearchLink();
            $l->name = $row['name'];
            $l->link = $row['link'];
            $l->size = isset($row['size']) ? $row['size'] : 0;
            if (isset($row['seeds'])) {
                $l->seeds = $row['seeds'];
            }
            $l->category = isset($row['cat']) ? $row['cat'] : '';
            $out[] = $l;
        }
        return $out;
    }
}
`

// A search through a plugin from another NAS in phpbox's real sandbox
// (needs root and QTS's PHP; skipped elsewhere).
func TestCodeSearchInSandbox(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("the sandbox needs root")
	}
	if _, err := os.Stat(phpbox.PHPPath); err != nil {
		t.Skip("no PHP at " + phpbox.PHPPath)
	}
	n := newFakeNet()
	s := newTestService(t, n)
	// uids of their own: phpbox's tests may run boxes at the same time, and
	// each uid may have one process
	box := phpbox.New(phpbox.Config{Dir: filepath.Join(t.TempDir(), "phpbox"), Version: "test", UIDBase: 59997})
	s.code = box
	if ok, code, reason := box.Check(context.Background()); !ok {
		t.Fatalf("%s: %s", code, reason)
	}
	meta := `{"name":"File search","class":"DcFileSearch","addon":"file.php","version":100,"interface":["ISearch"],"description":{"ENG":"Answers from a file"}}`
	answer := `[{"name":"Box\u0007 result","link":"magnet:?xt=urn:btih:` + hashA + `&dn=x&xs=http://e.example.org/&tr=udp%3A%2F%2Ft.example.org%3A1","size":42,"seeds":7,"cat":"Music"},` +
		`{"name":"Torrent result","link":"https://files.example.org/t.torrent","size":"1 KB"},{"name":"","link":"magnet:?xt=urn:btih:` + hashA + `"}]`
	pkg, err := phpbox.Inspect("f.addon", phpboxtest.Addon(phpboxtest.Files(map[string]string{"addon.json": meta, "file.php": fileAddon, "answer.json": answer}), phpboxtest.Key()))
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.store.InstallCode(pkg, Consent{User: "admin", Time: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	s.store.Update(id, &on, nil, nil)
	jid, err := s.Start("admin", true, Query{Text: "anything"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v := waitDone(t, s, "admin", jid)
	if v.Sources[0].State != "done" || len(v.Results) != 2 {
		t.Fatalf("%+v", v)
	}
	r := v.Results[0]
	if r.Title != "Box result" || r.Magnet != "magnet:?xt=urn:btih:"+hashA+"&dn=Box+result&tr=udp%3A%2F%2Ft.example.org%3A1" ||
		*r.Size != 42 || *r.Seeders != 7 || r.Category != "audio" {
		t.Errorf("%+v", r)
	}
	// Seeds the add-on left alone are unknown, not zero
	if t2 := v.Results[1]; t2.Ref == "" || t2.TorrentURL != "https://files.example.org/t.torrent" || *t2.Size != 1024 || t2.Seeders != nil || t2.Leechers != nil {
		t.Errorf("%+v", t2)
	}
}
