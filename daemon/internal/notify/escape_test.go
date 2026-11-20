package notify

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"downloadcenter/internal/core"
)

func (e *env) emitNamed(owner, name string) {
	ev := e.m.Emit(core.Event{Type: "task.completed", Owner: owner, Task: &core.EventTask{ID: "c9e1aa", Name: name, Kind: "url", Size: 1, Folder: "Download"}})
	e.s.Dispatch(ev)
}

// Task text cannot mention a whole channel or make links in Slack and
// Discord messages.
func TestChatMarkupEscaped(t *testing.T) {
	e := newEnv(t)
	slack, discord := &recorder{}, &recorder{}
	s1 := httptest.NewServer(http.HandlerFunc(slack.handler))
	defer s1.Close()
	s2 := httptest.NewServer(http.HandlerFunc(discord.handler))
	defer s2.Close()
	e.channel(t, "boss", "slack", map[string]string{}, map[string]string{"webhook_url": s1.URL})
	e.channel(t, "boss", "discord", map[string]string{}, map[string]string{"webhook_url": s2.URL})
	e.emitNamed("boss", "<!channel> <http://evil.example|x> [y](http://evil.example) @everyone")
	e.s.ProcessDeliveries()
	if slack.count() != 1 || discord.count() != 1 {
		t.Fatalf("deliveries: %d %d", slack.count(), discord.count())
	}
	var sm map[string]any
	json.Unmarshal(slack.bodies[0], &sm)
	text, _ := sm["text"].(string)
	if strings.Contains(text, "<!channel>") || strings.Contains(text, "<http") || !strings.Contains(text, "&lt;!channel&gt;") {
		t.Errorf("slack text %q", text)
	}
	var dm struct {
		Embeds   []map[string]any `json:"embeds"`
		Mentions map[string]any   `json:"allowed_mentions"`
	}
	json.Unmarshal(discord.bodies[0], &dm)
	desc, _ := dm.Embeds[0]["description"].(string)
	if strings.Contains(desc, "[y](http") || dm.Mentions == nil {
		t.Errorf("discord %q mentions %v", desc, dm.Mentions)
	}
}

// A failed delivery records the endpoint's host, never its address with
// the secret in it.
func TestDeliveryErrorHidesAddress(t *testing.T) {
	e := newEnv(t)
	c := e.channel(t, "boss", "slack", map[string]string{}, map[string]string{"webhook_url": "http://127.0.0.1:1/services/T000/SECRETPATH?token=SECRETTOKEN"})
	e.emitNamed("boss", "a")
	e.s.ProcessDeliveries()
	var msg sql.NullString
	e.m.DB().QueryRow(`SELECT error FROM deliveries WHERE channel_id = ?`, c.ID).Scan(&msg)
	if !msg.Valid || msg.String == "" || strings.Contains(msg.String, "SECRET") {
		t.Errorf("stored error %q", msg.String)
	}
	if !strings.Contains(scrubURLs("Post \"https://hooks.example/abc?k=SECRET\": EOF"), "hooks.example") {
		t.Error("host dropped")
	}
}

// A slow endpoint holds up only its own channel.
func TestSlowChannelAlone(t *testing.T) {
	e := newEnv(t)
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer slow.Close()
	fast := &recorder{}
	sf := httptest.NewServer(http.HandlerFunc(fast.handler))
	defer sf.Close()
	e.channel(t, "boss", "slack", map[string]string{}, map[string]string{"webhook_url": slow.URL})
	e.channel(t, "boss", "discord", map[string]string{}, map[string]string{"webhook_url": sf.URL})
	e.emitNamed("boss", "a")
	e.s.startDeliveries()
	deadline := time.Now().Add(2 * time.Second)
	for fast.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if fast.count() != 1 {
		t.Error("a slow channel held up another one")
	}
	// The slow channel is still sending: a new pass does not start it twice
	e.s.startDeliveries()
	e.s.mu.Lock()
	n := len(e.s.inFlight)
	e.s.mu.Unlock()
	if n != 1 {
		t.Errorf("channels in flight: %d", n)
	}
	close(release)
	e.s.sending.Wait()
}

// A regular user has a limited number of channels; an administrator not.
func TestChannelLimit(t *testing.T) {
	e := newEnv(t)
	alice, err := e.au.FromQTS("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	create := func() int {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"service":"ntfy","config":{"topic":"t"}}`))
		rec := httptest.NewRecorder()
		e.s.createChannel(rec, req, alice, "")
		return rec.Code
	}
	for i := 0; i < maxUserChannels; i++ {
		if c := create(); c != 200 {
			t.Fatalf("channel %d: %d", i, c)
		}
	}
	if c := create(); c != 409 {
		t.Errorf("one more: %d", c)
	}
}

// A LINE webhook event is acted on once, however often its signed body is
// sent again within the window.
func TestLineReplay(t *testing.T) {
	e := newEnv(t)
	if !e.s.firstSeen("ch/ev1") || e.s.firstSeen("ch/ev1") {
		t.Error("the same event was taken twice")
	}
	if !e.s.firstSeen("ch/ev2") || !e.s.firstSeen("other/ev1") {
		t.Error("another event was refused")
	}
	e.now = e.now.Add(replayWindow + time.Minute)
	if !e.s.firstSeen("ch/ev1") {
		t.Error("an event id is remembered forever")
	}
}

// A test notification and the Telegram calls keep only the host of an
// address in what they return or store.
func TestTestAndTelegramErrorsHideAddress(t *testing.T) {
	e := newEnv(t)
	c := e.channel(t, "boss", "slack", map[string]string{}, map[string]string{"webhook_url": "http://127.0.0.1:1/services/T000/SECRETPATH?token=SECRETTOKEN"})
	ok, msg := e.s.TestChannel(c)
	var stored string
	e.m.DB().QueryRow(`SELECT error FROM deliveries WHERE channel_id = ?`, c.ID).Scan(&stored)
	if ok || msg == "" || strings.Contains(msg, "SECRET") || strings.Contains(stored, "SECRET") {
		t.Errorf("test notification error %q, stored %q", msg, stored)
	}
	old := telegramAPI
	telegramAPI = "http://127.0.0.1:1"
	defer func() { telegramAPI = old }()
	err := tgCall(http.DefaultClient, "123456:SECRETBOT", "sendMessage", map[string]any{}, nil)
	if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "/bot") {
		t.Errorf("telegram error %v", err)
	}
}
