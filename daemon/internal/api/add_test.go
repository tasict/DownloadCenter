package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/store"
)

// Sources that cannot be added answer with their own error code and a
// readable message (translated like every other API message), never with
// the internal error text such as "magnet_invalid".
func TestAddErrors(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(core.New(db, dir), auth.New(db), dir, "test") // no engines: no URLs, no torrents
	p := &auth.Principal{User: "admin", Admin: true, Via: "session"}

	type answer struct {
		Error   map[string]string `json:"error"`
		Results []map[string]any  `json:"results"`
	}
	send := func(lang string, body any) (int, answer) {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		s.addTasks(&statusRecorder{ResponseWriter: rec, status: 200, lang: lang}, httptest.NewRequest("POST", "/x", bytes.NewReader(b)), p)
		var a answer
		json.Unmarshal(rec.Body.Bytes(), &a)
		return rec.Code, a
	}

	cases := []struct {
		source, code, msg, eng string
	}{
		{"magnet:?xt=urn:btih:nothex", "magnet_invalid", "磁力連結格式不正確", "Invalid magnet link format"},
		{"gopher://example.com/a", "url_not_supported", "不支援這種網址", ""},
		{"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567", "bt_unavailable", "這台 NAS 無法下載種子（缺少 BT 引擎）", ""},
		{"ftp://example.com/a.iso", "url_unavailable", "這台 NAS 無法下載這種網址（下載元件 dc-dl 無法使用）", ""},
	}
	var all []string
	for _, c := range cases {
		all = append(all, c.source)
		status, a := send("", map[string]any{"source": c.source})
		if status != 400 || a.Error["code"] != c.code || a.Error["message"] != c.msg {
			t.Errorf("%s: %d %v, want 400 %s %q", c.source, status, a.Error, c.code, c.msg)
		}
		_, a = send("ENG", map[string]any{"source": c.source})
		if m := a.Error["message"]; m == c.code || m == c.msg || m == "" || (c.eng != "" && m != c.eng) {
			t.Errorf("%s: English message %q", c.source, m)
		}
	}

	// Several sources: one result each, with the same codes
	status, a := send("", map[string]any{"sources": all})
	if status != 200 || len(a.Results) != len(cases) {
		t.Fatalf("several sources: %d, %d results", status, len(a.Results))
	}
	for i, c := range cases {
		e, _ := a.Results[i]["error"].(map[string]any)
		if e["code"] != c.code || e["message"] != c.msg {
			t.Errorf("result %d: %v, want %s %q", i, e, c.code, c.msg)
		}
	}

	// A .torrent upload that is not a torrent
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "x.torrent")
	fw.Write([]byte("not a torrent"))
	mw.Close()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/x", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	s.addTorrentUpload(&statusRecorder{ResponseWriter: rec, status: 200}, r, p)
	var up answer
	json.Unmarshal(rec.Body.Bytes(), &up)
	if rec.Code != 400 || up.Error["code"] != "torrent_invalid" || up.Error["message"] != "種子檔格式不正確" {
		t.Errorf("torrent upload: %d %v", rec.Code, up.Error)
	}
}
