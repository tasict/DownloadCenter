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
// readable message (English without X-DC-Lang, translated otherwise), never
// with the internal error text such as "magnet_invalid".
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
		source, code, msg, tch string
	}{
		{"magnet:?xt=urn:btih:nothex", "magnet_invalid", "Invalid magnet link format", "磁力連結格式不正確"},
		{"gopher://example.com/a", "url_not_supported", "This type of URL is not supported", "不支援這種網址"},
		{"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567", "bt_unavailable", "This NAS cannot download torrents (the BT engine is missing)", "這台 NAS 無法下載種子（缺少 BT 引擎）"},
		{"ftp://example.com/a.iso", "url_unavailable", "This NAS cannot download this kind of URL (the download component dc-dl is unavailable)", "這台 NAS 無法下載這種網址（下載元件 dc-dl 無法使用）"},
	}
	var all []string
	for _, c := range cases {
		all = append(all, c.source)
		status, a := send("", map[string]any{"source": c.source})
		if status != 400 || a.Error["code"] != c.code || a.Error["message"] != c.msg {
			t.Errorf("%s: %d %v, want 400 %s %q", c.source, status, a.Error, c.code, c.msg)
		}
		if _, a = send("TCH", map[string]any{"source": c.source}); a.Error["message"] != c.tch {
			t.Errorf("%s: Traditional Chinese message %q, want %q", c.source, a.Error["message"], c.tch)
		}
		if _, a = send("JPN", map[string]any{"source": c.source}); a.Error["message"] == c.msg || a.Error["message"] == "" {
			t.Errorf("%s: Japanese message %q", c.source, a.Error["message"])
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
	if rec.Code != 400 || up.Error["code"] != "torrent_invalid" || up.Error["message"] != "Invalid torrent file format" {
		t.Errorf("torrent upload: %d %v", rec.Code, up.Error)
	}
}

// Task errors and log lines that 1.0.x stored in the database are Traditional
// Chinese: the API still answers them in the reader's language.
func TestLegacyMessages(t *testing.T) {
	answer := func(lang string) map[string]any {
		rec := httptest.NewRecorder()
		JSON(&statusRecorder{ResponseWriter: rec, status: 200, lang: lang}, 200, map[string]any{
			"task": TaskJSON{State: core.StError, Error: map[string]any{"code": "dl_2", "message": "連線逾時（timeout after 30s）"}},
			"log":  []core.LogLine{{Msg: "錯誤：找不到這個任務"}, {Msg: "Switched to the new proxy, reconnecting"}},
		})
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	cases := []struct{ lang, err, log0, log1 string }{
		{"", "Connection timed out (timeout after 30s)", "Error: Task not found", "Switched to the new proxy, reconnecting"},
		{"TCH", "連線逾時（timeout after 30s）", "錯誤：找不到這個任務", "已更換代理，重新連線"},
		{"JPN", "接続がタイムアウトしました（timeout after 30s）", "エラー：このタスクが見つかりません", "プロキシを変更したため、再接続します"},
	}
	for _, c := range cases {
		out := answer(c.lang)
		task, _ := out["task"].(map[string]any)
		e, _ := task["error"].(map[string]any)
		log, _ := out["log"].([]any)
		if len(log) != 2 {
			t.Fatalf("%s: %v", c.lang, out)
		}
		l0, _ := log[0].(map[string]any)
		l1, _ := log[1].(map[string]any)
		if e["message"] != c.err || l0["msg"] != c.log0 || l1["msg"] != c.log1 {
			t.Errorf("%q: error %q, log %q / %q", c.lang, e["message"], l0["msg"], l1["msg"])
		}
	}
}
