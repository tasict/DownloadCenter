package api

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"downloadcenter/internal/auth"
)

var portraitClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}

// portrait serves the user's QTS profile picture (the one the QTS desktop
// shows). QTS hands it out by session id in the query string; fetching it
// here keeps the sid out of the page's image URLs.
func (s *Server) portrait(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	q := url.Values{"func": {"outputBgImgTh"}, "imgbgName": {"portrait.jpg"}, "sid": {p.SID}}
	resp, err := portraitClient.Get("http://127.0.0.1:58080/cgi-bin/userConfig.cgi?" + q.Encode())
	if err != nil {
		Error(w, 502, "unavailable", "QTS did not answer")
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	ct := http.DetectContentType(b)
	// No picture set: QTS answers with an empty or non-image body
	if resp.StatusCode != 200 || len(b) < 64 || !strings.HasPrefix(ct, "image/") || strings.Contains(ct, "svg") {
		w.Header().Set("Cache-Control", "private, max-age=300")
		Error(w, 404, "no_portrait", "No profile picture")
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(b)
}
