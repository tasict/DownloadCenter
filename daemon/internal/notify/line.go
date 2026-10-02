package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
)

func lineMessage(text string, buttons []Button) map[string]any {
	m := map[string]any{"type": "text", "text": truncate(text, 4900)}
	if len(buttons) > 0 {
		var items []any
		for _, b := range buttons {
			if len(items) >= 13 {
				break
			}
			items = append(items, map[string]any{"type": "action", "action": map[string]any{"type": "message", "label": truncate(b.Label, 20), "text": b.Command}})
		}
		m["quickReply"] = map[string]any{"items": items}
	}
	return m
}

func (s *Service) linePush(cl *http.Client, cfg map[string]string, text string, buttons []Button) (int, error) {
	return postJSON(cl, lineAPI+"/v2/bot/message/push", map[string]any{
		"to":       cfg["to"],
		"messages": []any{lineMessage(text, buttons)},
	}, map[string]string{"Authorization": "Bearer " + cfg["access_token"]})
}

func (s *Service) lineReply(cl *http.Client, cfg map[string]string, token, text string, buttons []Button) {
	postJSON(cl, lineAPI+"/v2/bot/message/reply", map[string]any{
		"replyToken": token,
		"messages":   []any{lineMessage(text, buttons)},
	}, map[string]string{"Authorization": "Bearer " + cfg["access_token"]})
}

// LineSignature is the x-line-signature of a body.
func LineSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// lineRoutes: the LINE Messaging API webhook of a channel. Public (LINE
// calls it), authenticated by the channel secret signature.
func (s *Service) lineRoutes(srv *api.Server) {
	srv.Route("POST /line/{id}", "", api.Public, func(w http.ResponseWriter, r *http.Request, _ *auth.Principal) {
		ch := s.channel(r.PathValue("id"))
		if ch == nil || ch.Service != "line" || !ch.Enabled {
			api.Error(w, 404, "not_found", "not found")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			api.Error(w, 400, "bad_request", "bad body")
			return
		}
		cfg := s.fullConfig(ch)
		want := LineSignature(cfg["channel_secret"], body)
		if cfg["channel_secret"] == "" || !hmac.Equal([]byte(want), []byte(r.Header.Get("X-Line-Signature"))) {
			api.Error(w, 401, "bad_signature", "signature mismatch")
			return
		}
		var payload struct {
			Events []struct {
				Type       string `json:"type"`
				ReplyToken string `json:"replyToken"`
				Source     struct {
					UserID string `json:"userId"`
				} `json:"source"`
				Message struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"message"`
			} `json:"events"`
		}
		json.Unmarshal(body, &payload)
		// Answer LINE quickly; commands run after
		api.OK(w, map[string]any{"ok": true})
		if !ch.Operate {
			return
		}
		go s.safe(func() {
			cl := s.client(!s.isAdmin(ch.Owner))
			for _, e := range payload.Events {
				if e.Type != "message" || e.Message.Type != "text" || e.Source.UserID == "" {
					continue
				}
				rep := s.chatCommand(ch, "line", e.Source.UserID, e.Message.Text)
				s.lineReply(cl, cfg, e.ReplyToken, rep.Reply, rep.Buttons)
			}
		})
	})
}
