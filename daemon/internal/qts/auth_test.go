package qts

import "testing"

// Only an explicit authPassed=0 ends a session: a busy QTS's page, an empty
// or unreadable body, or a pass without a user name is no answer.
func TestSIDAnswer(t *testing.T) {
	cases := []struct {
		body string
		ok   bool
	}{
		{`<QDocRoot><authPassed><![CDATA[0]]></authPassed></QDocRoot>`, true},
		{`<QDocRoot><authPassed><![CDATA[1]]></authPassed><username><![CDATA[alice]]></username><isAdmin>0</isAdmin></QDocRoot>`, true},
		{`<QDocRoot><authPassed><![CDATA[1]]></authPassed></QDocRoot>`, false},
		{``, false},
		{`<html><body><h2>503 Service Temporarily Overloaded</h2></body></html>`, false},
		{`not xml`, false},
	}
	for _, c := range cases {
		if err := sidAnswer(parseQDoc([]byte(c.body))); (err == nil) != c.ok {
			t.Errorf("%q: %v", c.body, err)
		}
	}
}

// What cannot be a session id is refused without asking authLogin.cgi.
func TestPlausibleSID(t *testing.T) {
	for sid, want := range map[string]bool{"abcd1234": true, "a-b_c.d~e+f/g=": true, "": false, "a b": false,
		"x;y": false, "x\x00": false, "é": false, string(make([]byte, 129)): false} {
		if PlausibleSID(sid) != want {
			t.Errorf("%q: %v", sid, !want)
		}
	}
	// Answered without a request to authLogin.cgi
	if a, err := ValidateSID("x;y", "", ""); err != nil || a["authPassed"] != "0" {
		t.Errorf("implausible sid: %v %v", a, err)
	}
}
