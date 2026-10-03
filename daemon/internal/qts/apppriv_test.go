package qts

import (
	"reflect"
	"strings"
	"testing"
)

func TestParsePrivList(t *testing.T) {
	out := "count = 3/3\nname:id:type:privilege\nadmin:0:1:1\nDOMAIN\\joe:0:4:1\nsandra:0:1:0\nfamily:0:2:1\n"
	got := parsePrivList(out, PrivLocalUser)
	want := []PrivEntry{{"admin", 1}, {`DOMAIN\joe`, 4}, {"family", 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if !got[2].Group() || got[0].Group() {
		t.Error("group rows")
	}
}

func TestAppPrivCalls(t *testing.T) {
	var calls []string
	defer StubAppriv(func(args ...string) (string, int, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "-C" && args[2] == "joe" {
			return "Permission Deny", 254, nil
		}
		return "", 0, nil
	})()
	if !AppAllowed("tasict") || AppAllowed("joe") || AppAllowed("") {
		t.Error("allowed answers")
	}
	AppGrant(PrivEntry{"family", PrivLocalGroup})
	want := []string{"-C -n tasict --app_name DownloadCenter", "-C -n joe --app_name DownloadCenter",
		"-A -n family --app_name DownloadCenter --is_group 1"}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls %q", calls)
	}
}

// The exit status of appriv -C on this firmware: 0 allowed, anything else
// denied. Read only, against the privilege list of the official package.
func TestAppprivExitStatus(t *testing.T) {
	if !AppPrivAvailable() {
		t.Skip("needs QTS application privileges")
	}
	out, code, err := appriv("-l", "--app_name", "DownloadStation", "--list_mode", "1", "--list_type", "1")
	if err != nil || code != 0 {
		t.Skip("DownloadStation is not registered")
	}
	var yes, no string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) != 4 || f[0] == "name" {
			continue
		}
		if f[3] == "1" && yes == "" {
			yes = f[0]
		}
		if f[3] == "0" && no == "" && !IsQTSAdmin(f[0]) {
			no = f[0]
		}
	}
	if yes == "" || no == "" {
		t.Skip("needs an allowed and a denied account")
	}
	if _, c, _ := appriv("-C", "-n", yes, "--app_name", "DownloadStation"); c != 0 {
		t.Errorf("%s is allowed, exit %d", yes, c)
	}
	if _, c, _ := appriv("-C", "-n", no, "--app_name", "DownloadStation"); c == 0 {
		t.Errorf("%s is denied, exit 0", no)
	}
}
