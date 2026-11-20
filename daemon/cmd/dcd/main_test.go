package main

import (
	"testing"

	"downloadcenter/internal/phpbox"
)

// A web request (ds.cgi, or a CGI environment) is always handled as one,
// even when the query string turns into the sandbox helper's argument.
func TestStartMode(t *testing.T) {
	env := func(kv map[string]string) func(string) string {
		return func(k string) string { return kv[k] }
	}
	cgi := map[string]string{"GATEWAY_INTERFACE": "CGI/1.1", "REQUEST_METHOD": "GET"}
	for _, c := range []struct {
		args []string
		env  map[string]string
		want string
	}{
		{[]string{"/x/v4web/ds.cgi", phpbox.HelperArg}, cgi, "cgi"},
		{[]string{"/x/bin/dcd", phpbox.HelperArg}, cgi, "cgi"},
		{[]string{"/x/bin/dcd", phpbox.HelperArg}, map[string]string{"REQUEST_METHOD": "GET"}, ""},
		{[]string{"/x/bin/dcd", phpbox.HelperArg}, nil, "helper"},
		{[]string{"/x/bin/dcd", "serve"}, nil, ""},
		{[]string{"/x/v4web/ds.cgi"}, nil, "cgi"},
	} {
		if got := startMode(c.args, env(c.env)); got != c.want {
			t.Errorf("%v %v: %q, want %q", c.args, c.env, got, c.want)
		}
	}
}
