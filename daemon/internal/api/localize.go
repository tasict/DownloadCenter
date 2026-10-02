package api

import (
	"downloadcenter/internal/core"
	"downloadcenter/internal/i18n"
)

// localize translates the backend-made texts inside known response shapes
// (a task's error, task log lines, an account's verification error) into the request's UI language. Names and
// other user content are never touched: only these fields carry messages.
func localize(w interface{}, v any) {
	lw, ok := w.(interface{ Lang() string })
	if !ok || i18n.Norm(lw.Lang()) == "" {
		return
	}
	lang := lw.Lang()
	task := func(j *TaskJSON) {
		if j.Error != nil {
			if m, ok := j.Error["message"].(string); ok {
				j.Error["message"] = i18n.T(lang, m)
			}
		}
	}
	logs := func(l []core.LogLine) {
		for i := range l {
			l[i].Msg = i18n.T(lang, l[i].Msg)
		}
	}
	var walk func(x any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case TaskJSON:
			task(&t)
			return t
		case []TaskJSON:
			for i := range t {
				task(&t[i])
			}
		case []core.LogLine:
			logs(t)
		case *core.Account:
			if t != nil {
				if m, ok := t.Info["error"].(string); ok {
					t.Info["error"] = i18n.T(lang, m)
				}
			}
		case []*core.Account:
			for _, a := range t {
				walk(a)
			}
		case map[string]any:
			for k, val := range t {
				t[k] = walk(val)
			}
		}
		return x
	}
	walk(v)
}
