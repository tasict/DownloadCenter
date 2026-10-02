package qts

import (
	"crypto/tls"
	"encoding/xml"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// AuthAnswer is the flattened XML answer of authLogin.cgi (leaf element
// name -> text; nested names are kept as their last segment).
type AuthAnswer map[string]string

func (a AuthAnswer) Passed() bool { return a["authPassed"] == "1" }

// User is the canonical user name of a passed answer.
func (a AuthAnswer) User() string {
	if u := a["username"]; u != "" {
		return u
	}
	return a["user"]
}

func (a AuthAnswer) IsAdmin() bool { return a["isAdmin"] == "1" }

var authClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // loopback only
		Proxy:           nil,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
}

var loggedShape sync.Once

// AuthCall calls authLogin.cgi (or another CGI of the same binary) from
// loopback. clientIP and agent are forwarded so QTS can attribute the
// request to the real client.
func AuthCall(cgi string, params url.Values, clientIP, agent string) (AuthAnswer, error) {
	bases := []string{"http://127.0.0.1:58080/cgi-bin/"}
	if port := GetCfg("Stunnel", "Port", "", "443"); port != "" {
		bases = append(bases, "https://127.0.0.1:"+port+"/cgi-bin/")
	}
	var lastErr error
	for _, base := range bases {
		req, err := http.NewRequest("POST", base+cgi, strings.NewReader(params.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if clientIP != "" {
			req.Header.Set("X-Forwarded-For", clientIP)
		}
		if agent != "" {
			req.Header.Set("User-Agent", agent)
		}
		resp, err := authClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != 200 {
			lastErr = errors.New("authLogin: HTTP " + resp.Status)
			continue
		}
		return parseQDoc(body), nil
	}
	return nil, lastErr
}

func parseQDoc(b []byte) AuthAnswer {
	out := AuthAnswer{}
	d := xml.NewDecoder(strings.NewReader(string(b)))
	d.Strict = false
	var stack []string
	var text strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			text.Reset()
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			if len(stack) > 0 {
				name := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if v := strings.TrimSpace(text.String()); v != "" || out[name] == "" {
					out[name] = v
				}
				text.Reset()
			}
		}
	}
	return out
}

// ValidateSID checks a QTS session id. The answer of a valid sid carries the
// user name and the administrator flag.
func ValidateSID(sid, clientIP, agent string) (AuthAnswer, error) {
	if sid == "" || len(sid) > 128 {
		return AuthAnswer{"authPassed": "0"}, nil
	}
	a, err := AuthCall("authLogin.cgi", url.Values{"sid": {sid}, "service": {"1"}}, clientIP, agent)
	if err != nil {
		return nil, err
	}
	if a.Passed() {
		loggedShape.Do(func() {
			var names []string
			for k := range a {
				names = append(names, k)
			}
			sort.Strings(names)
			log.Printf("auth: sid answer elements: %s", strings.Join(names, ","))
		})
	}
	return a, nil
}

// PasswordLogin signs in with a user name and password (V4 Misc/Login only;
// the package UI never sends credentials to the backend). pwd is plain text.
func PasswordLogin(user, pwdB64, clientIP, agent string) (AuthAnswer, error) {
	return AuthCall("authLogin.cgi", url.Values{
		"user":         {user},
		"pwd":          {pwdB64},
		"serviceKey":   {"1"},
		"client_app":   {"DownloadCenter"},
		"client_agent": {agent},
	}, clientIP, agent)
}

// Logout ends a QTS session.
func Logout(sid, clientIP, agent string) {
	AuthCall("authLogout.cgi", url.Values{"logout": {"1"}, "sid": {sid}}, clientIP, agent)
}
