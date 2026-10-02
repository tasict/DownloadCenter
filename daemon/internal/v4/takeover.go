package v4

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
	"downloadcenter/internal/qts"
)

// QdownloadLink is the firmware path the /downloadstation alias points at.
var QdownloadLink = "/home/httpd/cgi-bin/Qdownload"

// linkState describes the Qdownload path.
type linkState struct {
	exists  bool   // something is at the path
	symlink bool   // it is a symbolic link
	target  string // link target
	ours    bool   // the link points at our v4web folder
}

// decision is what enabling or disabling the takeover should do.
type decision int

const (
	keep   decision = iota // nothing to do
	link                   // create or replace the link
	unlink                 // remove our link
	refuse                 // not allowed now
)

// decide implements the takeover rules: the link is only ours while the
// official package is not installed or disabled; a link that points
// elsewhere is never replaced while the official package is enabled, and a
// real folder is never replaced.
func decide(enable, officialInstalled, officialEnabled bool, st linkState) decision {
	if !enable {
		if st.ours {
			return unlink
		}
		return keep
	}
	if officialInstalled && officialEnabled {
		return refuse
	}
	if st.ours {
		return keep
	}
	if st.exists && !st.symlink {
		return refuse
	}
	return link
}

func (s *service) webDir() string { return filepath.Join(s.root, "v4web") }

func (s *service) readLink() linkState {
	var st linkState
	fi, err := os.Lstat(QdownloadLink)
	if err != nil {
		return st
	}
	st.exists = true
	if fi.Mode()&os.ModeSymlink != 0 {
		st.symlink = true
		st.target, _ = os.Readlink(QdownloadLink)
		st.ours = filepath.Clean(st.target) == filepath.Clean(s.webDir())
	}
	return st
}

// prepareWebDir creates <root>/v4web with ds.cgi and qdownloadindex.cgi
// linked to the dcd binary (which forwards CGI requests to the daemon).
func (s *service) prepareWebDir() error {
	dir := s.webDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for _, name := range []string{"ds.cgi", "dsReq.cgi", "qdownloadindex.cgi"} {
		p := filepath.Join(dir, name)
		if t, err := os.Readlink(p); err == nil && t == "../bin/dcd" {
			continue
		}
		os.Remove(p)
		if err := os.Symlink("../bin/dcd", p); err != nil {
			return err
		}
	}
	return nil
}

func (s *service) apply(enable bool) error {
	inst, en := qts.QPKGInstalled("DownloadStation")
	st := s.readLink()
	switch decide(enable, inst, en, st) {
	case refuse:
		if inst && en {
			return errors.New("官方 Download Station 啟用中，請先停用或移除它")
		}
		return errors.New("Qdownload 是一般資料夾，不會覆蓋它")
	case link:
		if err := s.prepareWebDir(); err != nil {
			return err
		}
		if st.exists {
			if err := os.Remove(QdownloadLink); err != nil {
				return err
			}
		}
		return os.Symlink(s.webDir(), QdownloadLink)
	case unlink:
		return os.Remove(QdownloadLink)
	}
	return nil
}

func (s *service) takeoverState() map[string]any {
	inst, en := qts.QPKGInstalled("DownloadStation")
	st := s.readLink()
	return map[string]any{
		"official_installed": inst, "official_enabled": en,
		"linked": st.ours, "link_target": st.target,
		"can_link": decide(true, inst, en, st) != refuse,
		"enabled":  s.m.Settings().V4Takeover,
		"path":     "/downloadstation/V4/",
	}
}

func (s *service) registerTakeover() {
	s.prepareWebDir()
	// Re-apply the saved choice at start (only takes effect while the
	// official package is absent or disabled)
	if s.m.Settings().V4Takeover {
		if err := s.apply(true); err != nil {
			log.Printf("v4: takeover not applied: %v", err)
		}
	}
	s.srv.Route("GET /v4", "", api.AdminOnly|api.Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		api.OK(w, s.takeoverState())
	})
	s.srv.Route("POST /v4", "", api.AdminOnly|api.Session, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		var b struct {
			Enable bool `json:"enable"`
		}
		if err := api.Decode(r, &b); err != nil {
			api.Error(w, 400, "bad_request", err.Error())
			return
		}
		if err := s.apply(b.Enable); err != nil {
			api.Error(w, 409, "takeover_refused", err.Error())
			return
		}
		st := s.m.Settings()
		st.V4Takeover = b.Enable
		if err := s.m.SaveSettings(st); err != nil {
			api.Error(w, 500, "failed", err.Error())
			return
		}
		api.OK(w, s.takeoverState())
	})
}
