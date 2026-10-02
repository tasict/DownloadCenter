package hosters

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"downloadcenter/internal/core"
)

const agent = "DownloadCenter"

// call performs a request and decodes a JSON answer into out (when not nil).
// The HTTP status is returned even when decoding fails.
func (s *Service) call(req *http.Request, out any) (int, error) {
	req.Header.Set("User-Agent", "DownloadCenter/1.0")
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return 0, fmt.Errorf("無法連線到免空網站：%v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if out != nil && len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, fmt.Errorf("免空網站回應格式不正確（HTTP %d）", resp.StatusCode)
		}
	}
	return resp.StatusCode, nil
}

// num reads a JSON number that may also be a string.
func num(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	case json.Number:
		n, _ := x.Int64()
		return n
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	}
	return ""
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		x = strings.ToLower(x)
		return x == "1" || x == "true" || x == "yes" || x == "premium"
	}
	return false
}

// parseTime accepts unix seconds, RFC 3339 and "YYYY-MM-DD HH:MM:SS".
func parseTime(v any) int64 {
	if n := num(v); n > 100000000 {
		return n
	}
	sv := str(v)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, sv); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func nameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	n := path.Base(u.Path)
	if n == "/" || n == "." {
		return ""
	}
	if un, err := url.PathUnescape(n); err == nil {
		n = un
	}
	return strings.TrimSuffix(n, ".html")
}

// --- 1fichier (https://api.1fichier.com, Bearer API key, JSON POST) ---

func (s *Service) fichierPost(key, endpoint string, body any, out any) (int, error) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", s.base[OneFichier]+endpoint, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	return s.call(req, out)
}

func fichierErr(status int, msg string) error {
	low := strings.ToLower(msg)
	switch {
	case status == 401 || status == 403 && strings.Contains(low, "key") || strings.Contains(low, "not authenticated") || strings.Contains(low, "bad key"):
		return ErrBadLogin
	case strings.Contains(low, "not found") || strings.Contains(low, "deleted") || strings.Contains(low, "invalid url") || strings.Contains(low, "#606"):
		return ErrFileMissing
	case strings.Contains(low, "premium") || strings.Contains(low, "subscription") || strings.Contains(low, "expired"):
		return &accountError{ErrExpired, "expired"}
	case strings.Contains(low, "flood") || strings.Contains(low, "too many"):
		return ErrTryLater
	case strings.Contains(low, "traffic") || strings.Contains(low, "credit") || strings.Contains(low, "quota"):
		return &accountError{ErrTraffic, "traffic"}
	}
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", status)
	}
	return fmt.Errorf("1fichier：%s", msg)
}

func (s *Service) fichierResolve(key, raw string) (*core.Resolved, error) {
	var r map[string]any
	st, err := s.fichierPost(key, "/download/get_token.cgi", map[string]any{"url": raw}, &r)
	if err != nil {
		return nil, err
	}
	if strings.ToUpper(str(r["status"])) != "OK" || str(r["url"]) == "" {
		return nil, fichierErr(st, str(r["message"]))
	}
	res := &core.Resolved{URL: str(r["url"]), ExpiresAt: time.Now().Add(30 * time.Minute).Unix()}
	var info map[string]any
	if _, err := s.fichierPost(key, "/file/info.cgi", map[string]any{"url": raw}, &info); err == nil {
		res.Name = str(info["filename"])
		res.Size = num(info["size"])
	}
	return res, nil
}

func (s *Service) fichierVerify(key string) (map[string]any, error) {
	var r map[string]any
	st, err := s.fichierPost(key, "/user/info.cgi", map[string]any{}, &r)
	if err != nil {
		return nil, err
	}
	if strings.ToUpper(str(r["status"])) == "KO" || st >= 400 {
		return nil, fichierErr(st, str(r["message"]))
	}
	info := map[string]any{"traffic_left": int64(-1)}
	offer := str(r["offer"])
	if offer == "" {
		offer = str(r["account_type"])
	}
	premium := false
	var exp int64
	for _, k := range []string{"premium_expire", "premium_end", "subscription_end", "offer_end", "access_expire", "expire"} {
		if v, ok := r[k]; ok {
			if t := parseTime(v); t > 0 {
				exp = t
			}
		}
	}
	if exp > time.Now().Unix() || truthy(r["premium"]) || strings.Contains(strings.ToLower(offer), "premium") || strings.Contains(strings.ToLower(offer), "access") {
		premium = true
	}
	if offer == "" {
		offer = map[bool]string{true: "Premium", false: "Free"}[premium]
	}
	info["plan"] = offer
	info["premium"] = premium
	info["expires_at"] = exp
	if c, ok := r["available_credits_in_gigabyte"]; ok {
		info["traffic_left"] = int64(numF(c) * (1 << 30))
	}
	if exp > 0 && exp < time.Now().Unix() {
		info["error"] = ErrExpired.Error()
	}
	return info, nil
}

func numF(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

// --- Rapidgator (API v2, login -> token) ---

type rgToken struct {
	token string
	at    time.Time
	sum   string
}

var rgFileID = regexp.MustCompile(`/file/([0-9a-zA-Z]+)`)

type rgAnswer struct {
	Response json.RawMessage `json:"response"`
	Status   any             `json:"status"`
	Details  any             `json:"details"`
}

func (s *Service) rgGet(endpoint string, q url.Values) (map[string]any, int, string, error) {
	req, _ := http.NewRequest("GET", s.base[Rapidgator]+endpoint+"?"+q.Encode(), nil)
	var a rgAnswer
	httpSt, err := s.call(req, &a)
	if err != nil {
		return nil, httpSt, "", err
	}
	st := int(num(a.Status))
	if st == 0 {
		st = httpSt
	}
	var resp map[string]any
	json.Unmarshal(a.Response, &resp)
	return resp, st, str(a.Details), nil
}

func rgErr(status int, details string) error {
	low := strings.ToLower(details)
	switch {
	case status == 401 || strings.Contains(low, "invalid login") || strings.Contains(low, "wrong") || strings.Contains(low, "password"):
		return ErrBadLogin
	case status == 404 || strings.Contains(low, "not found") || strings.Contains(low, "deleted"):
		return ErrFileMissing
	case strings.Contains(low, "traffic") || strings.Contains(low, "bandwidth") || strings.Contains(low, "limit"):
		return &accountError{ErrTraffic, "traffic"}
	case strings.Contains(low, "premium") || strings.Contains(low, "expired"):
		return &accountError{ErrExpired, "expired"}
	}
	if details == "" {
		details = fmt.Sprintf("錯誤 %d", status)
	}
	return fmt.Errorf("Rapidgator：%s", details)
}

// rgLogin returns a session token (cached 1 h per account and password).
func (s *Service) rgLogin(a *core.Account, pass string, force bool) (string, map[string]any, error) {
	s.mu.Lock()
	t, ok := s.rgTokens[a.ID]
	s.mu.Unlock()
	if ok && !force && t.sum == sum(pass) && time.Since(t.at) < time.Hour {
		return t.token, nil, nil
	}
	resp, st, det, err := s.rgGet("/user/login", url.Values{"login": {a.Username}, "password": {pass}})
	if err != nil {
		return "", nil, err
	}
	if st != 200 || str(resp["token"]) == "" {
		return "", nil, rgErr(st, det)
	}
	tok := str(resp["token"])
	s.mu.Lock()
	s.rgTokens[a.ID] = rgToken{token: tok, at: time.Now(), sum: sum(pass)}
	s.mu.Unlock()
	user, _ := resp["user"].(map[string]any)
	return tok, user, nil
}

func rgInfo(user map[string]any) map[string]any {
	premium := truthy(user["is_premium"])
	exp := parseTime(user["premium_end_time"])
	info := map[string]any{"premium": premium, "expires_at": exp, "traffic_left": int64(-1)}
	info["plan"] = map[bool]string{true: "Premium", false: "Free"}[premium]
	if tr, ok := user["traffic"].(map[string]any); ok {
		if l, ok := tr["left"]; ok {
			info["traffic_left"] = num(l)
		}
	}
	return info
}

func (s *Service) rgResolve(a *core.Account, pass, raw string) (*core.Resolved, error) {
	m := rgFileID.FindStringSubmatch(raw)
	if m == nil {
		return nil, ErrFileMissing
	}
	for attempt := 0; attempt < 2; attempt++ {
		tok, user, err := s.rgLogin(a, pass, attempt > 0)
		if err != nil {
			return nil, err
		}
		if user != nil {
			info := rgInfo(user)
			if !info["premium"].(bool) {
				return nil, &accountError{ErrExpired, "expired"}
			}
			if tl := info["traffic_left"].(int64); tl == 0 {
				return nil, &accountError{ErrTraffic, "traffic"}
			}
		}
		resp, st, det, err := s.rgGet("/file/download", url.Values{"token": {tok}, "file_id": {m[1]}})
		if err != nil {
			return nil, err
		}
		if st == 401 && attempt == 0 {
			continue // session expired: log in again
		}
		if st != 200 || str(resp["download_url"]) == "" {
			return nil, rgErr(st, det)
		}
		res := &core.Resolved{URL: str(resp["download_url"]), ExpiresAt: time.Now().Add(2 * time.Hour).Unix(), Name: nameFromURL(raw)}
		if fi, st2, _, err := s.rgGet("/file/info", url.Values{"token": {tok}, "file_id": {m[1]}}); err == nil && st2 == 200 {
			if f, ok := fi["file"].(map[string]any); ok {
				if n := str(f["name"]); n != "" {
					res.Name = n
				}
				res.Size = num(f["size"])
			}
		}
		return res, nil
	}
	return nil, ErrBadLogin
}

func (s *Service) rgVerify(a *core.Account, pass string) (map[string]any, error) {
	_, user, err := s.rgLogin(a, pass, true)
	if err != nil {
		return nil, err
	}
	info := rgInfo(user)
	if !info["premium"].(bool) {
		info["error"] = "這個 Rapidgator 帳號不是 Premium，無法下載"
	}
	return info, nil
}

// --- Real-Debrid (REST 1.0, Bearer token) ---

func rdErr(status int, r map[string]any) error {
	code := num(r["error_code"])
	msg := str(r["error"])
	switch code {
	case 8, 9, 12, 13, 14:
		return ErrBadLogin
	case 20:
		return &accountError{ErrExpired, "expired"}
	case 24, 25:
		return ErrFileMissing
	case 16, 28:
		return ErrHostNotSup
	case 34, 21:
		return ErrTryLater
	case 36:
		return &accountError{ErrTraffic, "traffic"}
	}
	if status == 401 {
		return ErrBadLogin
	}
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", status)
	}
	return fmt.Errorf("Real-Debrid：%s", msg)
}

func (s *Service) rdResolve(token, raw string) (*core.Resolved, error) {
	req, _ := http.NewRequest("POST", s.base[RealDebrid]+"/unrestrict/link", strings.NewReader(url.Values{"link": {raw}}.Encode()))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var r map[string]any
	st, err := s.call(req, &r)
	if err != nil {
		return nil, err
	}
	if st >= 400 || str(r["download"]) == "" {
		return nil, rdErr(st, r)
	}
	return &core.Resolved{URL: str(r["download"]), Name: str(r["filename"]), Size: num(r["filesize"]),
		ExpiresAt: time.Now().Add(6 * time.Hour).Unix()}, nil
}

func (s *Service) rdVerify(token string) (map[string]any, error) {
	req, _ := http.NewRequest("GET", s.base[RealDebrid]+"/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	var r map[string]any
	st, err := s.call(req, &r)
	if err != nil {
		return nil, err
	}
	if st >= 400 {
		return nil, rdErr(st, r)
	}
	premium := str(r["type"]) == "premium"
	exp := parseTime(r["expiration"])
	info := map[string]any{"plan": map[bool]string{true: "Premium", false: "Free"}[premium], "premium": premium,
		"expires_at": exp, "traffic_left": int64(-1)}
	if !premium {
		info["error"] = "這個 Real-Debrid 帳號沒有 Premium，無法下載"
	}
	return info, nil
}

// --- AllDebrid (v4, apikey) ---

type adAnswer struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (s *Service) adGet(endpoint, key string, q url.Values) (map[string]any, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("agent", agent)
	if key != "" {
		q.Set("apikey", key)
	}
	req, _ := http.NewRequest("GET", s.base[AllDebrid]+endpoint+"?"+q.Encode(), nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	var a adAnswer
	if _, err := s.call(req, &a); err != nil {
		return nil, err
	}
	if a.Status != "success" {
		code, msg := "", ""
		if a.Error != nil {
			code, msg = a.Error.Code, a.Error.Message
		}
		return nil, adErr(code, msg)
	}
	var d map[string]any
	json.Unmarshal(a.Data, &d)
	return d, nil
}

func adErr(code, msg string) error {
	switch {
	case strings.HasPrefix(code, "AUTH_"):
		return ErrBadLogin
	case code == "MUST_BE_PREMIUM" || code == "FREE_TRIAL_LIMIT_REACHED" || code == "PREMIUM_ONLY":
		return &accountError{ErrExpired, "expired"}
	case code == "LINK_DOWN" || code == "LINK_NOT_FOUND" || code == "LINK_IS_MISSING" || code == "LINK_PASS_PROTECTED":
		return ErrFileMissing
	case code == "LINK_HOST_NOT_SUPPORTED" || code == "LINK_HOST_UNAVAILABLE" || code == "LINK_NOT_SUPPORTED":
		return ErrHostNotSup
	case code == "LINK_HOST_LIMIT_REACHED" || code == "LINK_TOO_MANY_DOWNLOADS" || code == "LINK_ERROR":
		return &accountError{ErrTraffic, "traffic"}
	case code == "LINK_TEMPORARY_UNAVAILABLE" || code == "MAINTENANCE":
		return ErrTryLater
	}
	if msg == "" {
		msg = code
	}
	if msg == "" {
		msg = "未知的錯誤"
	}
	return fmt.Errorf("AllDebrid：%s", msg)
}

func (s *Service) adResolve(key, raw string) (*core.Resolved, error) {
	d, err := s.adGet("/link/unlock", key, url.Values{"link": {raw}})
	if err != nil {
		return nil, err
	}
	link := str(d["link"])
	if link == "" {
		if d["delayed"] != nil {
			return nil, ErrTryLater
		}
		return nil, errors.New("AllDebrid 沒有回傳下載位址")
	}
	return &core.Resolved{URL: link, Name: str(d["filename"]), Size: num(d["filesize"]),
		ExpiresAt: time.Now().Add(6 * time.Hour).Unix()}, nil
}

func (s *Service) adVerify(key string) (map[string]any, error) {
	d, err := s.adGet("/user", key, nil)
	if err != nil {
		return nil, err
	}
	u, _ := d["user"].(map[string]any)
	if u == nil {
		u = d
	}
	premium := truthy(u["isPremium"])
	exp := parseTime(u["premiumUntil"])
	info := map[string]any{"plan": map[bool]string{true: "Premium", false: "Free"}[premium], "premium": premium,
		"expires_at": exp, "traffic_left": int64(-1)}
	if !premium {
		info["error"] = "這個 AllDebrid 帳號沒有 Premium，無法下載"
	}
	return info, nil
}

// --- supported domains of the debrid services (cached 24 h) ---

func (s *Service) debridCovers(kind string, a *core.Account, host string) bool {
	s.mu.Lock()
	hl, ok := s.hosts[kind]
	s.mu.Unlock()
	if !ok || time.Since(hl.at) > 24*time.Hour {
		domains, err := s.fetchDomains(kind, s.b.AccountSecret(a.ID))
		if err != nil {
			if !ok {
				// Do not retry on every call while the service is unreachable
				hl = hostList{domains: map[string]bool{}, at: time.Now().Add(-23 * time.Hour)}
			} else {
				hl.at = time.Now().Add(-23 * time.Hour)
			}
		} else {
			hl = hostList{domains: domains, at: time.Now()}
		}
		s.mu.Lock()
		s.hosts[kind] = hl
		s.mu.Unlock()
	}
	return domainIn(host, hl.domains)
}

func (s *Service) fetchDomains(kind, key string) (map[string]bool, error) {
	out := map[string]bool{}
	add := func(v any) {
		if l, ok := v.([]any); ok {
			for _, d := range l {
				if ds := strings.ToLower(strings.TrimSpace(str(d))); ds != "" {
					out[strings.TrimPrefix(ds, "www.")] = true
				}
			}
		}
	}
	switch kind {
	case RealDebrid:
		req, _ := http.NewRequest("GET", s.base[RealDebrid]+"/hosts/domains", nil)
		var l []any
		st, err := s.call(req, &l)
		if err != nil {
			return nil, err
		}
		if st >= 400 {
			return nil, fmt.Errorf("HTTP %d", st)
		}
		add(l)
	case AllDebrid:
		d, err := s.adGet("/hosts/domains", key, nil)
		if err != nil {
			return nil, err
		}
		add(d["hosts"])
		add(d["redirectors"])
	}
	return out, nil
}
