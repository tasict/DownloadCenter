package api

import (
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"downloadcenter/internal/auth"
)

// A multipart upload larger than the limit is refused without spooling
// anything to a temporary file, on every route that takes torrent files.
func TestUploadLimit(t *testing.T) {
	s := fixServer(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	p := &auth.Principal{User: "bob", Via: "session"}
	for name, h := range map[string]Handler{"check": s.checkTasks, "probe": s.probe, "torrent": s.addTorrentUpload} {
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		var sent int64
		go func() {
			fw, _ := mw.CreateFormFile("file", "big.torrent")
			chunk := make([]byte, 1<<20)
			for i := 0; i < 200; i++ {
				if _, err := fw.Write(chunk); err != nil {
					break
				}
				sent += int64(len(chunk))
			}
			mw.Close()
			pw.Close()
		}()
		req := httptest.NewRequest("POST", "/x", pr)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		h(rec, req, p)
		pr.CloseWithError(io.ErrClosedPipe)
		if rec.Code != 413 {
			t.Errorf("%s: status %d %s", name, rec.Code, rec.Body.String())
		}
		filepath.Walk(tmp, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				t.Errorf("%s: spooled %s (%d bytes)", name, path, info.Size())
			}
			return nil
		})
	}
}
