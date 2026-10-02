package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/store"
	"downloadcenter/internal/torrent"
)

const sampleConf = `[v3]
upgraded=true
[bt]
dht=true
nat=false
lsd=false
port_from=6881
port_to=6889
encrypt=true
max_num=7
max_down_rate=100
max_up_rate=50
share_ratio=2.5
share_time=60
max_conn=200
torrent_max_conn=40
torrent_max_up=10
upnp_forward=true
proxy_type=2
proxy_auth=true
proxy_hostname=proxy.example
proxy_port=1080
proxy_username=u
proxy_password=c2VjcmV0
peer_mode=3
peer_id=TR
peer_version=2.94
peer_agent=Transmission/2.94
[ftp]
max_num=4
max_down_rate=0
[http]
max_num=9
max_down_rate=300
[global]
schedule_enable=true
schedule0=000000000000000000000000
schedule1=111111111111111111111111
schedule2=222222222222222222222222
schedule3=111111111111111111111111
schedule4=111111111111111111111111
schedule5=111111111111111111111111
schedule6=121212121212121212121212
down_folder=Download
move_folder=Multimedia
`

func TestApplySettings(t *testing.T) {
	c := parseINI(strings.NewReader(sampleConf))
	s := core.DefaultSettings()
	r := applySettings(c, &s, false, true)
	if !s.Schedule.Enabled || s.Schedule.Days[6] != strings.Repeat("0", 24) || s.Schedule.Days[0] != strings.Repeat("1", 24) ||
		s.Schedule.Days[1] != strings.Repeat("2", 24) || s.Schedule.Days[5] != "121212121212121212121212" {
		t.Errorf("schedule rotation: %v", s.Schedule.Days)
	}
	if s.HTTP.MaxNum != 9 || s.HTTP.MaxDown != 300 || s.FTP.MaxNum != 4 || s.BT.MaxNum != 7 || s.BT.MaxUp != 50 {
		t.Errorf("limits: %+v %+v %+v", s.HTTP, s.FTP, s.BT)
	}
	if s.Torrent.SeedRatio != 2.5 || s.Torrent.SeedTime != 60 || !s.Torrent.Encrypt || s.Torrent.LSD || !s.Torrent.UPnP ||
		s.Torrent.PortFrom != 6881 || s.Torrent.PeerMode != 3 || s.Torrent.PeerID != "TR" {
		t.Errorf("torrent: %+v", s.Torrent)
	}
	if r.TempShare != "Download" || r.MoveShare != "Multimedia" {
		t.Errorf("folders: %+v", r)
	}
	if len(s.Proxy.Profiles) != 0 || s.Proxy.BT != "" || len(r.Warnings) == 0 {
		t.Errorf("SOCKS5 must not be imported without engine support: %+v %v", s.Proxy, r.Warnings)
	}
	s2 := core.DefaultSettings()
	r2 := applySettings(c, &s2, true, false)
	bt := s2.Proxy.Profile(s2.Proxy.BT)
	if bt == nil || bt.Type != "socks5" || bt.Host != "proxy.example" || bt.User != "u" || r2.ProxyPass != "secret" || r2.ProxyID != bt.ID {
		t.Errorf("socks5: %+v %q", s2.Proxy, r2.ProxyPass)
	}
	if s2.Torrent.PortFrom == 6881 {
		t.Errorf("ports must be kept while the official package is enabled")
	}
}

// TestDryRun reads the real official files (read-only copies) and prints
// counts only.
func TestDryRun(t *testing.T) {
	if os.Getenv("DC_IMPORT_DRYRUN") == "" {
		t.Skip("set DC_IMPORT_DRYRUN=1")
	}
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := core.New(db, dir)
	im := New(m, auth.New(db), dir)
	d := im.Detect()
	t.Logf("available=%v official=%v settings=%v volumes=%d missing_users=%d warnings=%d",
		d.Available, d.Official, d.Settings, len(d.Volumes), len(d.UsersMissing), len(d.Warnings))
	for i, v := range d.Volumes {
		bt, url, sel, temps, torrents := 0, 0, 0, 0, 0
		for _, tk := range v.tasks {
			if tk.Type == 0 {
				bt++
				if _, err := os.Stat(filepath.Join(tk.Volume, ".torrent", tk.Hash+".torrent")); err == nil {
					torrents++
				}
			} else {
				url++
			}
			if tk.Selected != nil {
				sel++
			}
			if tk.State != 5 {
				if root := im.resolveFolder("", tk.Temp); root != "" && officialTempFolder(shareRootOf(root), tk.Hash) != "" {
					temps++
					of := officialTempFolder(shareRootOf(root), tk.Hash)
					if b, err := os.ReadFile(filepath.Join(tk.Volume, ".torrent", tk.Hash+".torrent")); err == nil {
						if meta, err := torrent.Parse(b); err == nil {
							_, serr := os.Stat(filepath.Join(of, meta.Name))
							t.Logf("layout: data root inside official temp folder = %v (infohash matches = %v)", serr == nil, meta.InfoHash == tk.Hash)
						}
					}
				}
			}
		}
		t.Logf("volume %d: tasks=%d unfinished=%d completed=%d accounts=%d bt=%d url=%d partial_selection=%d torrent_files=%d temp_found=%d",
			i, v.Tasks, v.Unfinished, v.Completed, v.Accounts, bt, url, sel, torrents, temps)
	}
	f, err := os.Open(ConfPath)
	if err == nil {
		c := parseINI(f)
		f.Close()
		t.Logf("ds.conf sections=%d", len(c))
	}
}
