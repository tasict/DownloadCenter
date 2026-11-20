package auth

import (
	"fmt"
	"testing"

	"downloadcenter/internal/qts"
)

func stubSID(t *testing.T, answer func(sid string) qts.AuthAnswer) *int {
	calls := new(int)
	old := validateSID
	validateSID = func(sid, ip, agent string) (qts.AuthAnswer, error) {
		*calls++
		return answer(sid), nil
	}
	t.Cleanup(func() { validateSID = old })
	return calls
}

// A sid QTS refused is not asked about again for a while, a client that
// sends many unknown sids stops costing calls to QTS, and an implausible
// sid costs none.
func TestSIDChecksBounded(t *testing.T) {
	s, _ := newTestService(t, &fakeQTS{})
	calls := stubSID(t, func(string) qts.AuthAnswer { return qts.AuthAnswer{"authPassed": "0"} })
	for i := 0; i < 3; i++ {
		if _, err := s.FromSID("bad1", "192.0.2.7", ""); err != ErrNotSignedIn {
			t.Fatalf("refused sid: %v", err)
		}
	}
	if *calls != 1 {
		t.Errorf("the same refused sid asked %d times", *calls)
	}
	for _, sid := range []string{"a b", "x;y", "\x00", string(make([]byte, 200))} {
		s.FromSID(sid, "192.0.2.7", "")
	}
	if *calls != 1 {
		t.Errorf("implausible sids asked: %d", *calls)
	}
	*calls = 0
	for i := 0; i < 100; i++ {
		s.FromSID(fmt.Sprintf("rnd%d", i), "192.0.2.8", "")
	}
	if *calls > sidMissesPerMinute {
		t.Errorf("one client made %d calls to QTS", *calls)
	}
	if _, err := s.FromSID("rnd-next", "192.0.2.8", ""); err != ErrAuthBackend {
		t.Errorf("over the limit: %v (must not read as a sign-out)", err)
	}
	// Another client is not affected
	*calls = 0
	s.FromSID("other", "192.0.2.9", "")
	if *calls != 1 {
		t.Error("another client was limited")
	}
}

// Failures are counted per client address, never against loopback (all
// requests look like loopback when the proxy's address cannot be believed),
// and the counters stay bounded.
func TestFailureCounting(t *testing.T) {
	s, _ := newTestService(t, &fakeQTS{})
	for i := 0; i < 25; i++ {
		s.FromToken("dct_x_y", "127.0.0.1")
		s.FromToken("dct_x_y", "::1")
	}
	if _, err := s.FromToken("dct_x_y", "127.0.0.1"); err == ErrRateLimited {
		t.Error("loopback locked out")
	}
	for i := 0; i < 20; i++ {
		s.FromToken("dct_x_y", "192.0.2.1")
	}
	if _, err := s.FromToken("dct_x_y", "192.0.2.1"); err != ErrRateLimited {
		t.Errorf("guessing client not stopped: %v", err)
	}
	// V4 password sign-in: 10 failures
	for i := 0; i < 10; i++ {
		if !s.LoginAllowed("192.0.2.2") {
			t.Fatalf("stopped after %d failures", i)
		}
		s.LoginFailed("192.0.2.2")
		s.LoginFailed("127.0.0.1")
	}
	if s.LoginAllowed("192.0.2.2") {
		t.Error("10 failed sign-ins not stopped")
	}
	if !s.LoginAllowed("127.0.0.1") || !s.LoginAllowed("192.0.2.3") {
		t.Error("loopback or another client stopped")
	}
	for i := 0; i < maxBuckets+50; i++ {
		s.fail(fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255))
	}
	if n := len(s.fails); n > maxBuckets {
		t.Errorf("%d failure counters kept", n)
	}
}
