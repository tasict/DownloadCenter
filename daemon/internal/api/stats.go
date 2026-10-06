package api

import (
	"sort"
	"strings"

	"downloadcenter/internal/auth"
)

// Names for the usage statistics (see Count). Every name is built from
// the code's own vocabulary; request data only picks one of them.

// tokenPresets are the permission sets the token window offers (PRESETS in
// shared/web/js/settings-more.js; a test keeps the two equal).
var tokenPresets = map[string][]string{
	"read": {"tasks:read", "stats:read", "events:read"},
	"add":  {"tasks:read", "tasks:add", "stats:read", "events:read"},
	"full": {"tasks:read", "tasks:add", "tasks:control", "tasks:remove", "stats:read", "events:read"},
}

// tokenKeys describes a new token (after checkToken): who made it, its
// preset, scopes, expiry and limits. Never its name, owner or values.
func tokenKeys(t *auth.Token, admin bool) []string {
	keys := []string{"tok_created", "tok_by_user"}
	if admin {
		keys[1] = "tok_by_admin"
	}
	keys = append(keys, "tok_preset_"+presetOf(t.Scopes))
	for _, sc := range t.Scopes {
		keys = append(keys, "tok_scope_"+strings.ReplaceAll(sc, ":", "_"))
	}
	exp := "never"
	if t.ExpiresAt > 0 {
		switch (t.ExpiresAt - t.Created + 43200) / 86400 {
		case 30:
			exp = "30"
		case 90:
			exp = "90"
		case 365:
			exp = "365"
		default:
			exp = "other"
		}
	}
	keys = append(keys, "tok_exp_"+exp)
	for k, on := range map[string]bool{
		"tok_all_tasks":   t.Tasks == "all",
		"tok_folders":     len(t.Folders) > 0,
		"tok_sources":     len(t.Sources) > 0,
		"tok_ip_allow":    len(t.IPAllow) > 0,
		"tok_rate_custom": t.RateLimit != 120,
	} {
		if on {
			keys = append(keys, k)
		}
	}
	return keys
}

// presetOf names the preset a set of scopes equals, or "custom".
func presetOf(scopes []string) string {
	have := append([]string(nil), scopes...)
	sort.Strings(have)
	for name, p := range tokenPresets {
		want := append([]string(nil), p...)
		sort.Strings(want)
		if strings.Join(have, " ") == strings.Join(want, " ") {
			return name
		}
	}
	return "custom"
}

// routeKey names a REST API route after its pattern: "GET /tasks/{id}/files"
// is api_get_tasks_id_files. Too long or odd patterns become api_other.
func routeKey(pattern string) string {
	var b strings.Builder
	b.WriteString("api_")
	sep := false
	for _, c := range strings.ToLower(pattern) {
		switch {
		case c == '{' || c == '}':
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
			if sep && !strings.HasSuffix(b.String(), "_") {
				b.WriteByte('_')
			}
			sep = false
			b.WriteRune(c)
		default:
			sep = true
		}
	}
	k := b.String()
	if len(k) > 40 || k == "api_" {
		return "api_other"
	}
	return k
}

// clientOf sorts a User-Agent into a fixed set of client kinds; the header
// itself never leaves the NAS.
func clientOf(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case l == "":
		return "none"
	case strings.Contains(l, "homeassistant"):
		return "homeassistant"
	case strings.Contains(l, "powershell"):
		return "powershell"
	case strings.HasPrefix(l, "mozilla/"):
		return "browser"
	case strings.HasPrefix(l, "curl/"):
		return "curl"
	case strings.HasPrefix(l, "wget/"):
		return "wget"
	case strings.HasPrefix(l, "python"), strings.HasPrefix(l, "aiohttp"), strings.HasPrefix(l, "httpx"):
		return "python"
	case l == "node", strings.HasPrefix(l, "node/"), strings.HasPrefix(l, "node-fetch"), strings.HasPrefix(l, "undici"), strings.HasPrefix(l, "axios/"):
		return "node"
	case strings.HasPrefix(l, "go-http-client/"):
		return "go"
	}
	return "other"
}
