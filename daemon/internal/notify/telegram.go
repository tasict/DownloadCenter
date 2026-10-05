package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"downloadcenter/internal/core"
)

const unlinkedText = "這個聊天帳號還沒有連結 Download Center。請在 Download Center 的「設定 › 通知與整合」按「連結我的帳號」，再把 /link 與 6 位數配對碼傳給我。"

// tgCall calls a Bot API method.
func tgCall(cl *http.Client, token, method string, body any, out any) error {
	if token == "" {
		return errors.New("No bot token set")
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", telegramAPI+"/bot"+token+"/"+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		// Never leak the bot token through URL errors
		return errors.New(strings.ReplaceAll(err.Error(), token, "***"))
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&r); err != nil {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if !r.OK {
		return fmt.Errorf("Telegram: %s", r.Description)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func tgKeyboard(buttons []Button) any {
	if len(buttons) == 0 {
		return nil
	}
	var row []map[string]string
	for _, b := range buttons {
		if len(b.Command) <= 64 {
			row = append(row, map[string]string{"text": b.Label, "callback_data": b.Command})
		}
	}
	if len(row) == 0 {
		return nil
	}
	return map[string]any{"inline_keyboard": [][]map[string]string{row}}
}

func tgSend(cl *http.Client, token, chatID, text string, buttons []Button) error {
	body := map[string]any{"chat_id": chatID, "text": truncate(text, 4000), "disable_web_page_preview": true}
	if kb := tgKeyboard(buttons); kb != nil {
		body["reply_markup"] = kb
	}
	return tgCall(cl, token, "sendMessage", body, nil)
}

// sendTelegram sends to the configured chat, or to every linked chat
// whose account may see the event.
func (s *Service) sendTelegram(cl *http.Client, ch *Channel, cfg map[string]string, msg *Message) (int, error) {
	var targets []string
	if cfg["chat_id"] != "" {
		targets = append(targets, cfg["chat_id"])
	} else {
		for _, l := range s.links(ch.ID) {
			if msg.Event != nil && msg.Event.Owner != "" && msg.Event.Owner != l.QTSUser && !s.isAdmin(l.QTSUser) {
				continue
			}
			if chat, ok := s.chatOf(ch, l.ChatUser); ok {
				targets = append(targets, chat)
			}
		}
	}
	if len(targets) == 0 {
		return 0, errors.New("No recipient: enter a chat id, or link a chat account first")
	}
	var firstErr error
	for _, t := range targets {
		if err := tgSend(cl, cfg["bot_token"], t, msg.Text(), msg.Buttons); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return 0, firstErr
	}
	return 200, nil
}

// chatOf returns the private chat of a linked user (the user id doubles as
// the private chat id in Telegram).
func (s *Service) chatOf(ch *Channel, chatUser string) (string, bool) {
	if v, ok := ch.State["chat:"+chatUser].(string); ok && v != "" {
		return v, true
	}
	return chatUser, chatUser != ""
}

// reconcilePollers runs one long-polling goroutine per enabled Telegram
// channel with operate on.
func (s *Service) reconcilePollers() {
	want := map[string]bool{}
	for _, c := range s.loadChannels(`enabled = 1 AND operate = 1 AND service = 'telegram'`) {
		want[c.ID] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stopCh:
		return
	default:
	}
	for id, stop := range s.pollers {
		if !want[id] {
			close(stop)
			delete(s.pollers, id)
		}
	}
	for id := range want {
		if _, ok := s.pollers[id]; !ok {
			stop := make(chan struct{})
			s.pollers[id] = stop
			go s.poll(id, stop)
		}
	}
}

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
	Callback *struct {
		ID      string     `json:"id"`
		From    tgUser     `json:"from"`
		Data    string     `json:"data"`
		Message *tgMessage `json:"message"`
	} `json:"callback_query"`
}

type tgUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type tgMessage struct {
	MessageID int64  `json:"message_id"`
	From      tgUser `json:"from"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Text     string `json:"text"`
	Caption  string `json:"caption"`
	Document *struct {
		FileID   string `json:"file_id"`
		FileName string `json:"file_name"`
		FileSize int64  `json:"file_size"`
	} `json:"document"`
}

func (s *Service) poll(id string, stop chan struct{}) {
	backoff := 5 * time.Second
	for {
		select {
		case <-stop:
			return
		default:
		}
		ch := s.channel(id)
		if ch == nil || !ch.Enabled || !ch.Operate {
			return
		}
		cfg := s.fullConfig(ch)
		offset, _ := toInt(ch.State["tg_offset"])
		cl := s.client(!s.isAdmin(ch.Owner))
		cl.Timeout = 70 * time.Second
		var updates []tgUpdate
		err := tgCall(cl, cfg["bot_token"], "getUpdates", map[string]any{"offset": offset, "timeout": 50, "allowed_updates": []string{"message", "callback_query"}}, &updates)
		if err != nil {
			log.Printf("notify: telegram %s: %v", id, err)
			select {
			case <-stop:
				return
			case <-time.After(backoff):
			}
			if backoff < 5*time.Minute {
				backoff *= 2
			}
			continue
		}
		backoff = 5 * time.Second
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			s.safe(func() { s.handleTelegram(ch, cfg, cl, u) })
		}
		if len(updates) > 0 {
			s.setState(ch, "tg_offset", offset)
		}
	}
}

func (s *Service) handleTelegram(ch *Channel, cfg map[string]string, cl *http.Client, u tgUpdate) {
	token := cfg["bot_token"]
	if u.Callback != nil {
		tgCall(cl, token, "answerCallbackQuery", map[string]any{"callback_query_id": u.Callback.ID}, nil)
		if u.Callback.Message == nil {
			return
		}
		chat := strconv.FormatInt(u.Callback.Message.Chat.ID, 10)
		user := strconv.FormatInt(u.Callback.From.ID, 10)
		r := s.chatCommand(ch, "tg", user, u.Callback.Data)
		tgSend(cl, token, chat, r.Reply, r.Buttons)
		return
	}
	m := u.Message
	if m == nil {
		return
	}
	chat := strconv.FormatInt(m.Chat.ID, 10)
	user := strconv.FormatInt(m.From.ID, 10)
	if m.Chat.ID == m.From.ID {
		s.setState(ch, "chat:"+user, chat)
	}
	if m.Document != nil && strings.HasSuffix(strings.ToLower(m.Document.FileName), ".torrent") {
		tgSend(cl, token, chat, s.telegramTorrent(ch, cl, token, user, m.Document.FileID, m.Document.FileSize), nil)
		return
	}
	text := m.Text
	if text == "" {
		text = m.Caption
	}
	if text == "" {
		return
	}
	r := s.chatCommand(ch, "tg", user, text)
	tgSend(cl, token, chat, r.Reply, r.Buttons)
}

// chatCommand runs a command from a chat service: /link for unlinked users,
// everything else with the linked account's rights.
func (s *Service) chatCommand(ch *Channel, kind, chatUser, text string) Reply {
	text = strings.TrimSpace(text)
	if cmd, rest, _ := strings.Cut(text, " "); strings.EqualFold(strings.Split(cmd, "@")[0], "/link") {
		user, err := s.Link(ch.ID, chatUser, rest)
		if err != nil {
			return Reply{Reply: zh(err.Error())}
		}
		return Reply{OK: true, Reply: "已連結 Download Center 帳號 " + user + "。傳送 /help 看可以用的指令。"}
	}
	p := s.linked(ch.ID, chatUser)
	if p == nil {
		return Reply{Reply: unlinkedText}
	}
	return s.Run(p, kind+":"+ch.ID+":"+chatUser, text)
}

func (s *Service) telegramTorrent(ch *Channel, cl *http.Client, token, user, fileID string, size int64) string {
	p := s.linked(ch.ID, user)
	if p == nil {
		return unlinkedText
	}
	if !p.Can("tasks:add") || !p.SourceAllowed("torrent") {
		return "沒有權限加入種子。"
	}
	if size > 16<<20 {
		return "種子檔太大。"
	}
	var f struct {
		FilePath string `json:"file_path"`
	}
	if err := tgCall(cl, token, "getFile", map[string]any{"file_id": fileID}, &f); err != nil {
		return "無法取得檔案：" + zh(err.Error())
	}
	resp, err := cl.Get(telegramAPI + "/file/bot" + token + "/" + (&url.URL{Path: f.FilePath}).EscapedPath())
	if err != nil {
		return "無法下載檔案"
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	res, err := s.m.AddTorrent(data, core.AddOptions{Owner: p.User, Admin: p.Admin, AutoRemove: "default", Caller: "Chat"})
	if err != nil {
		return "無法加入：" + addError(err)
	}
	if res.Merged {
		return "已併入既有任務：" + res.Name
	}
	return "已加入：" + res.Name + "（" + s.queuePos(res.ID) + "）"
}
