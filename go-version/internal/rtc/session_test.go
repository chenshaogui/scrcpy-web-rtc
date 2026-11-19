package rtc

import (
	"strings"
	"testing"
	"time"
)

func TestSessionCreateOffer(t *testing.T) {
	signals := make(chan Signal, 32)
	cb := SessionCallbacks{
		SendSignal: func(s Signal) { signals <- s },
		Log:        func(f string, a ...interface{}) { t.Logf(f, a...) },
	}
	s, err := New(cb, nil)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer s.Close()

	if err := s.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	select {
	case sig := <-signals:
		if sig.T != "rtc-offer" {
			t.Fatalf("first signal = %q, want rtc-offer", sig.T)
		}
		if !strings.Contains(sig.SDP, "v=0") || !strings.Contains(sig.SDP, "H264") {
			t.Errorf("offer SDP looks invalid:\n%s", sig.SDP)
		}
		t.Logf("offer generated, %d bytes", len(sig.SDP))
	case <-time.After(10 * time.Second):
		t.Fatal("no rtc-offer signal within 10s (would hang the browser at 'connecting')")
	}
}

// The offer must carry the configured TURN servers: without them the browser
// can never mint a relay candidate, so remote viewers (different NAT, e.g. FN
// Connect) get no media path at all — webrtc.mjs sends iceServers here too.
func TestOfferCarriesTurnServers(t *testing.T) {
	signals := make(chan Signal, 32)
	cb := SessionCallbacks{
		SendSignal: func(s Signal) { signals <- s },
		Log:        func(f string, a ...interface{}) { t.Logf(f, a...) },
	}
	turn := []ICEServer{{URLs: []string{"turn:turn.example.com:3478"}, Username: "u", Credential: "p"}}
	s, err := New(cb, turn)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer s.Close()
	if err := s.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	select {
	case sig := <-signals:
		var found bool
		for _, srv := range sig.ICEServers {
			for _, u := range srv.URLs {
				if u == "turn:turn.example.com:3478" && srv.Username == "u" && srv.Credential == "p" {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("rtc-offer iceServers missing TURN credentials: %+v", sig.ICEServers)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no rtc-offer signal within 10s")
	}
}
