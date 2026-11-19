// Package rtc wraps pion/webrtc into a session API equivalent to webrtc.mjs's
// createRtcSession: a sendonly H264/Opus peer connection plus control/shell
// data channels, driven by JSON signaling messages.
package rtc

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"

	"github.com/tiann/scrcpy-webrtc-go/internal/rtp"
)

type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

type Signal struct {
	T          string      `json:"t"`
	SDP        string      `json:"sdp,omitempty"`
	Candidate  interface{} `json:"candidate,omitempty"`
	ICEServers []ICEServer `json:"iceServers,omitempty"`
}

// SessionCallbacks carries events back to the connection handler.
type SessionCallbacks struct {
	SendSignal        func(Signal)
	OnControl         func(map[string]interface{})
	OnShell           func(map[string]interface{})
	OnKeyframeRequest func()
	OnBitrateEstimate func(bps int)
	Log               func(format string, args ...interface{})
}

// Session is one WebRTC peer connection.
type Session struct {
	pc *webrtc.PeerConnection

	videoTrack  *webrtc.TrackLocalStaticRTP
	audioTrack  *webrtc.TrackLocalStaticRTP
	videoSender *webrtc.RTPSender
	packetizer  *rtp.H264Packetizer
	audioState  *rtp.StreamState

	controlDC *webrtc.DataChannel
	shellDC   *webrtc.DataChannel

	cb SessionCallbacks

	// full ICE list (STUN + configured TURN) — the offer must carry the TURN
	// credentials too, or the browser can never mint a relay candidate and
	// remote viewers behind a different NAT stay black (webrtc.mjs parity).
	iceServers []ICEServer

	mu        sync.Mutex
	connected bool
	lastPLI   time.Time
	closed    bool
	gen       uint32
}

var defaultICEServers = []ICEServer{
	{URLs: []string{"stun:stun.cloudflare.com:3478"}},
	{URLs: []string{"stun:stun.l.google.com:19302"}},
}

func logf(cb SessionCallbacks, format string, args ...interface{}) {
	if cb.Log != nil {
		cb.Log(format, args...)
	}
}

// New creates a peer connection with H264 (PT 96) and Opus (PT 111) sendonly
// transceivers and the two data channels. The caller then calls Start().
func New(cb SessionCallbacks, turnServers []ICEServer) (*Session, error) {
	m := &webrtc.MediaEngine{}
	// H264 constrained baseline, matching werift's offer parameters.
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: "video/H264", ClockRate: 90000,
			Channels: 0, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
			RTCPFeedback: []webrtc.RTCPFeedback{
				{Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "goog-remb"},
			},
		},
		PayloadType: webrtc.PayloadType(rtp.VideoPT),
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: "audio/opus", ClockRate: 48000, Channels: 2,
		},
		PayloadType: webrtc.PayloadType(rtp.AudioPT),
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}

	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		return nil, err
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir))

	ice := append([]ICEServer{}, defaultICEServers...)
	ice = append(ice, turnServers...)
	webrtcICEServers := make([]webrtc.ICEServer, 0, len(ice))
	for _, s := range ice {
		webrtcICEServers = append(webrtcICEServers, webrtc.ICEServer{
			URLs: s.URLs, Username: s.Username, Credential: s.Credential,
		})
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: webrtcICEServers})
	if err != nil {
		return nil, err
	}

	videoTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: "video/H264", ClockRate: 90000},
		"video", "scrcpy",
	)
	if err != nil {
		pc.Close()
		return nil, err
	}
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: "audio/opus", ClockRate: 48000, Channels: 2},
		"audio", "scrcpy",
	)
	if err != nil {
		pc.Close()
		return nil, err
	}
	videoSender, err := pc.AddTrack(videoTrack)
	if err != nil {
		pc.Close()
		return nil, err
	}
	if _, err := pc.AddTrack(audioTrack); err != nil {
		pc.Close()
		return nil, err
	}

	s := &Session{
		pc: pc, videoTrack: videoTrack, audioTrack: audioTrack, videoSender: videoSender,
		packetizer: rtp.NewH264Packetizer(), audioState: rtp.NewStreamState(),
		cb: cb, iceServers: ice,
	}

	// Data channels (ordered), same labels as the Node implementation.
	controlDC, err := pc.CreateDataChannel("control", nil)
	if err != nil {
		pc.Close()
		return nil, err
	}
	s.controlDC = controlDC
	controlDC.OnMessage(func(msg webrtc.DataChannelMessage) {
		dispatchJSON(cb.OnControl, msg.Data)
	})
	shellDC, err := pc.CreateDataChannel("shell", nil)
	if err != nil {
		pc.Close()
		return nil, err
	}
	s.shellDC = shellDC
	shellDC.OnMessage(func(msg webrtc.DataChannelMessage) {
		dispatchJSON(cb.OnShell, msg.Data)
	})

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		if cb.SendSignal != nil {
			cb.SendSignal(Signal{T: "rtc-ice", Candidate: c.ToJSON()})
		}
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		logf(cb, "webrtc: %s", st)
		s.mu.Lock()
		wasConnected := s.connected
		s.connected = st == webrtc.PeerConnectionStateConnected
		s.mu.Unlock()
		if s.connected && !wasConnected && cb.OnKeyframeRequest != nil {
			cb.OnKeyframeRequest()
		}
	})

	return s, nil
}

func dispatchJSON(fn func(map[string]interface{}), data []byte) {
	if fn == nil {
		return
	}
	var m map[string]interface{}
	if json.Unmarshal(data, &m) == nil {
		fn(m)
	}
}

// Start creates the offer and sends it through the signaling channel.
func (s *Session) Start() error {
	offer, err := s.pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err := s.pc.SetLocalDescription(offer); err != nil {
		return err
	}
	// Wait for ICE gathering so the offer includes candidates (werift did this
	// implicitly by signaling on candidate events; browsers accept trickle too).
	if s.cb.SendSignal != nil {
		s.cb.SendSignal(Signal{T: "rtc-offer", SDP: s.pc.LocalDescription().SDP, ICEServers: s.iceServers})
	}
	// Start RTCP feedback reader for keyframe events.
	go s.readRTCP()
	return nil
}

func (s *Session) readRTCP() {
	for {
		buf := make([]byte, 1500)
		n, _, err := s.videoSender.Read(buf)
		if err != nil {
			return
		}
		// Parse RTCP minimally: detect PLI (PT 206) / FIR / REMB app feedback.
		if n < 8 {
			continue
		}
		pt := buf[1]
		switch pt {
		case 206, 204: // PLI / FIR
			s.mu.Lock()
			now := time.Now()
			if now.Sub(s.lastPLI) > time.Second {
				s.lastPLI = now
				s.mu.Unlock()
				if s.cb.OnKeyframeRequest != nil {
					s.cb.OnKeyframeRequest()
				}
			} else {
				s.mu.Unlock()
			}
		}
	}
}

// HandleAnswer applies a remote SDP answer.
func (s *Session) HandleAnswer(sdp string) error {
	return s.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp})
}

// HandleIce adds a trickled ICE candidate.
func (s *Session) HandleIce(candidate map[string]interface{}) error {
	raw, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	var c webrtc.ICECandidateInit
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	return s.pc.AddICECandidate(c)
}

// SendVideoPacket writes one scrcpy video packet to the RTP track.
func (s *Session) SendVideoPacket(config bool, keyframe bool, pts uint64, data []byte) {
	if config {
		s.packetizer.SetConfig(data)
		return
	}
	ts := rtp.VideoTimestamp(pts)
	for _, p := range s.packetizer.Packetize(data, ts, keyframe) {
		raw, err := p.Marshal()
		if err == nil {
			_, _ = s.videoTrack.Write(raw)
		}
	}
}

// SendAudioPacket writes one Opus frame to the RTP track.
func (s *Session) SendAudioPacket(config bool, pts uint64, data []byte) {
	if config {
		return // opus ID header, not an RTP payload
	}
	p := s.audioState.Packet(rtp.AudioPT, data, rtp.AudioTimestamp(pts), false)
	raw, err := p.Marshal()
	if err == nil {
		_, _ = s.audioTrack.Write(raw)
	}
}

// SendShell sends a JSON object on the shell data channel. Returns false if
// the channel is not open so callers can fall back to signaling.
func (s *Session) SendShell(obj interface{}) bool {
	if s.shellDC == nil || s.shellDC.ReadyState() != webrtc.DataChannelStateOpen {
		return false
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return false
	}
	return s.shellDC.SendText(string(raw)) == nil
}

// Connected reflects the peer connection state.
func (s *Session) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected
}

// Close closes the peer connection.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	return s.pc.Close()
}

var errNotOpen = errors.New("rtc: data channel not open")

func (s *Session) sendControlRaw(data []byte) error {
	if s.controlDC == nil || s.controlDC.ReadyState() != webrtc.DataChannelStateOpen {
		return errNotOpen
	}
	return s.controlDC.Send(data)
}

// SendControlJSON sends a JSON control object through the data channel.
func (s *Session) SendControlJSON(obj interface{}) error {
	raw, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("rtc: marshal control: %w", err)
	}
	return s.sendControlRaw(raw)
}
