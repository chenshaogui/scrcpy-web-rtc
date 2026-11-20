package server

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/tiann/scrcpy-webrtc-go/internal/adb"
	"github.com/tiann/scrcpy-webrtc-go/internal/rtc"
	"github.com/tiann/scrcpy-webrtc-go/internal/scrcpy"
)

// onMessage handles every JSON control message from the viewer.
func (s *Session) onMessage(data []byte, isBinary bool) {
	if isBinary {
		return
	}
	var msg map[string]interface{}
	if json.Unmarshal(data, &msg) != nil {
		return
	}
	t, _ := msg["t"].(string)

	switch {
	case len(t) >= 6 && t[:6] == "shell-":
		s.handleShell(msg)
	case t == "rtc-start":
		s.startRTC()
	case t == "rtc-answer":
		if sdp, ok := msg["sdp"].(string); ok {
			s.mu.Lock()
			r := s.rtc
			s.mu.Unlock()
			if r != nil {
				_ = r.HandleAnswer(sdp)
			}
		}
	case t == "rtc-ice":
		if cand, ok := msg["candidate"].(map[string]interface{}); ok {
			s.mu.Lock()
			r := s.rtc
			s.mu.Unlock()
			if r != nil {
				_ = r.HandleIce(cand)
			}
		}
	case t == "quality":
		s.handleQuality(msg)
	case t == "options":
		s.handleOptions(msg)
	case t == "encoder":
		s.handleEncoder(msg, false)
	case t == "audio-encoder":
		s.handleEncoder(msg, true)
	case t == "video-path":
		mode, _ := msg["mode"].(string)
		s.mu.Lock()
		if mode == "rtc" {
			s.videoPath = "rtc"
		} else {
			s.videoPath = "ws"
		}
		s.mu.Unlock()
		s.log("video path: %s", mode)
	case t == "rtc-video":
		enabled := true
		if v, ok := msg["enabled"].(bool); ok {
			enabled = v
		}
		s.mu.Lock()
		s.rtcVideo = enabled
		s.mu.Unlock()
	default:
		s.handleControl(msg)
	}
}

func (s *Session) startRTC() {
	s.mu.Lock()
	if s.rtc != nil {
		_ = s.rtc.Close()
		s.rtc = nil
	}
	gen := s.rtcGen + 1
	s.rtcGen = gen
	s.mu.Unlock()

	turnServers := s.srv.turnServers()
	if s.closedFlag() {
		return
	}

	cb := rtc.SessionCallbacks{
		SendSignal: func(sig rtc.Signal) {
			data, _ := json.Marshal(sig)
			_ = s.conn.SendText(string(data))
		},
		OnControl:         func(m map[string]interface{}) { s.handleControl(m) },
		OnShell:           func(m map[string]interface{}) { s.handleShell(m) },
		OnKeyframeRequest: func() { s.onKeyframeRequest() },
		OnBitrateEstimate: func(bps int) { s.onBitrateEstimate(bps) },
		Log:               s.log,
	}
	session, err := rtc.New(cb, turnServers)
	if err != nil {
		s.log("webrtc init error: %v", err)
		return
	}
	s.mu.Lock()
	if gen != s.rtcGen || s.closed {
		s.mu.Unlock()
		_ = session.Close()
		return
	}
	s.rtc = session
	lastConfig := append([]byte(nil), s.lastConfig...)
	s.mu.Unlock()

	// Replay the cached SPS/PPS so the browser can decode the next keyframe
	// even if the config frame arrived before this peer existed (parity with
	// server.mjs). The packetizer prepends it to the next keyframe.
	if len(lastConfig) > 0 {
		session.SendVideoPacket(true, false, 0, lastConfig)
	}

	if err := session.Start(); err != nil {
		s.log("webrtc offer error: %v", err)
	}
}

// onKeyframeRequest runs when the Pion peer first reaches "connected". Any
// packets written before DTLS came up were dropped by Pion, including the
// scrcpy config and first IDR. Replay the cached IDR immediately so the browser
// has something decodable, then ask the device for a fresh one as well.
func (s *Session) onKeyframeRequest() {
	s.mu.Lock()
	rtcSession := s.rtc
	cfg := append([]byte(nil), s.lastConfig...)
	kf := append([]byte(nil), s.lastKeyframe...)
	pts := s.lastKeyframePTS
	s.mu.Unlock()
	if rtcSession != nil && len(kf) > 0 {
		func() {
			defer func() { _ = recover() }()
			if len(cfg) > 0 {
				rtcSession.SendVideoPacket(true, false, 0, cfg)
			}
			rtcSession.SendVideoPacket(false, true, pts, kf)
		}()
	}
	s.requestKeyframe()
}

func (s *Session) closedFlag() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Session) requestKeyframe() {
	s.mu.Lock()
	c := s.control
	s.mu.Unlock()
	if c != nil {
		_, _ = c.Write(scrcpy.ResetVideo())
	}
}

func (s *Session) handleQuality(msg map[string]interface{}) {
	preset, _ := msg["preset"].(string)
	s.mu.Lock()
	if preset == "auto" {
		s.autoQuality = true
		s.mu.Unlock()
		return
	}
	if _, ok := qualityPresets[preset]; ok {
		s.autoQuality = false
		changed := preset != s.preset
		s.preset = preset
		ready := s.streamReady
		s.mu.Unlock()
		s.log("quality preset -> %s", preset)
		if changed && ready {
			s.requestRestart()
		}
		return
	}
	s.mu.Unlock()
}

func (s *Session) handleOptions(msg map[string]interface{}) {
	opts, ok := msg["options"].(map[string]interface{})
	if !ok {
		return
	}
	clean := sanitizeOptions(opts)
	s.mu.Lock()
	before, _ := json.Marshal(s.custom)
	after, _ := json.Marshal(clean)
	s.custom = clean
	changed := string(before) != string(after)
	ready := s.streamReady
	s.mu.Unlock()
	if changed {
		s.log("custom options -> %s", string(after))
		if ready {
			s.requestRestart()
		}
	}
}

var allowedOptionTypes = map[string]interface{}{
	"maxSize": "number", "videoBitRate": "number", "videoCodec": []string{"h264", "h265", "av1"},
	"videoEncoder": "string", "videoCodecOptions": "string", "maxFps": "number",
	"audio": "bool", "audioCodec": []string{"raw", "opus", "aac"}, "audioEncoder": "string",
	"audioBitRate": "number", "audioCodecOptions": "string", "audioSource": []string{"output", "mic"},
	"captureOrientation": "string", "angle": "number", "screenOffTimeout": "number",
	"displayId": "number", "newDisplay": "string", "crop": "string", "showTouches": "bool",
	"stayAwake": "bool", "powerOn": "bool", "powerOffOnClose": "bool", "clipboardAutosync": "bool",
	"control": "bool", "tunnelForward": "bool",
	"logLevel": []string{"verbose", "debug", "info", "warn", "error"},
}

func sanitizeOptions(in map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range in {
		spec, ok := allowedOptionTypes[k]
		if !ok || v == nil || v == "" {
			continue
		}
		switch sp := spec.(type) {
		case string:
			switch sp {
			case "number":
				if n, ok := num(v); ok {
					out[k] = n
				}
			case "bool":
				if b, ok := v.(bool); ok {
					out[k] = b
				}
			case "string":
				out[k] = fmt.Sprint(v)
			}
		case []string:
			if str, ok := v.(string); ok {
				for _, allowed := range sp {
					if str == allowed {
						out[k] = str
						break
					}
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Session) handleEncoder(msg map[string]interface{}, audio bool) {
	if list, _ := msg["list"].(bool); list {
		s.requestEncoders()
		return
	}
	name, _ := msg["name"].(string)
	s.mu.Lock()
	changed := false
	if audio {
		codec, _ := msg["codec"].(string)
		if codec == "" {
			codec = codecFromEncoderName(name)
		}
		if codec == "" {
			codec = "opus"
		}
		changed = name != s.audioEncoder || codec != s.audioCodec
		s.audioEncoder = name
		s.audioCodec = codec
		s.sendSignalLocked(map[string]interface{}{"type": "audio-encoder", "name": name, "codec": codec})
	} else {
		changed = name != s.videoEncoder
		s.videoEncoder = name
		s.sendSignalLocked(map[string]interface{}{"type": "encoder", "name": name})
	}
	ready := s.streamReady
	s.mu.Unlock()
	if changed && ready {
		s.requestRestart()
	}
}

func (s *Session) requestEncoders() {
	s.mu.Lock()
	if s.encodersReady {
		payload := s.encoderPayloadLocked()
		s.mu.Unlock()
		s.sendSignal(payload)
		return
	}
	if s.encoderProbe {
		s.mu.Unlock()
		return
	}
	s.encoderProbe = true
	transport := s.transport
	s.mu.Unlock()

	if transport == nil {
		s.mu.Lock()
		s.encoderProbe = false
		s.mu.Unlock()
		s.sendSignal(map[string]interface{}{
			"type": "encoders", "list": []scrcpy.Encoder{}, "error": "device stream is not ready",
		})
		return
	}

	go func() {
		select {
		case <-time.After(2 * time.Second):
		case <-s.done:
			return
		}
		list, err := s.probeEncoders(transport)
		s.mu.Lock()
		s.encoderProbe = false
		if err == nil {
			s.encoders = list
			s.encodersReady = true
		}
		payload := s.encoderPayloadLocked()
		s.mu.Unlock()
		if err != nil {
			s.log("encoder probe failed: %v", err)
			payload["error"] = err.Error()
		} else {
			s.log("device reports %d encoder(s)", len(list))
		}
		s.sendSignal(payload)
	}()
}

func (s *Session) encoderPayloadLocked() map[string]interface{} {
	list := append([]scrcpy.Encoder(nil), s.encoders...)
	if list == nil {
		list = []scrcpy.Encoder{}
	}
	return map[string]interface{}{
		"type": "encoders", "list": list,
		"current": s.videoEncoder, "currentAudio": s.audioEncoder,
	}
}

func (s *Session) probeEncoders(transport *adb.Transport) ([]scrcpy.Encoder, error) {
	if s.closedFlag() {
		return nil, errDisconnected
	}
	return scrcpy.ListEncoders(transport, s.srv.cfg.ServerBin)
}

func (s *Session) sendSignalLocked(v interface{}) {
	data, _ := json.Marshal(v)
	_ = s.conn.SendText(string(data))
}

// handleControl encodes control messages and enqueues them on the serial worker.
func (s *Session) handleControl(msg map[string]interface{}) {
	t, _ := msg["t"].(string)
	switch t {
	case "touch":
		action, _ := msg["a"].(string)
		var a byte
		switch action {
		case "down":
			a = scrcpy.ActionDown
		case "up":
			a = scrcpy.ActionUp
		case "move":
			a = scrcpy.ActionMove
		default:
			a = scrcpy.ActionMove
		}
		x, _ := num(msg["x"])
		y, _ := num(msg["y"])
		s.mu.Lock()
		w, h := s.videoW, s.videoH
		s.mu.Unlock()
		if w == 0 || h == 0 {
			return
		}
		data := scrcpy.InjectTouch(a, uint32(float64(w)*x), uint32(float64(h)*y), w, h, pressureFor(a), buttonsFor(a))
		s.enqueueControl(func() { s.inject(data) })
	case "scroll":
		x, _ := num(msg["x"])
		y, _ := num(msg["y"])
		dx, _ := num(msg["dx"])
		dy, _ := num(msg["dy"])
		s.mu.Lock()
		w, h := s.videoW, s.videoH
		s.mu.Unlock()
		data := scrcpy.InjectScroll(uint32(float64(w)*x), uint32(float64(h)*y), w, h, float32(dx), float32(dy), 0)
		s.enqueueControl(func() { s.inject(data) })
	case "key":
		action, _ := msg["a"].(string)
		code, _ := num(msg["code"])
		var a uint32 = 0
		if action == "up" {
			a = 1
		}
		data := scrcpy.InjectKeyCode(a, uint32(code), 0, 0)
		s.enqueueControl(func() { s.inject(data) })
	case "text":
		text, _ := msg["text"].(string)
		data := scrcpy.InjectText(text)
		s.enqueueControl(func() { s.inject(data) })
	case "clipboard":
		text, _ := msg["text"].(string)
		s.mu.Lock()
		if text == "" || text == s.clipFromDevice {
			s.mu.Unlock()
			return
		}
		s.clipToDevice = text
		s.clipSeq++
		seq := s.clipSeq
		s.mu.Unlock()
		data := scrcpy.SetClipboard(seq, false, text)
		s.enqueueControl(func() { s.inject(data) })
	}
}

func pressureFor(action byte) uint32 {
	if action == scrcpy.ActionUp {
		return 0
	}
	return 1
}

func buttonsFor(action byte) uint32 {
	if action == scrcpy.ActionUp {
		return 0
	}
	return 1
}

// requestRestart closes the current scrcpy streams and flags a restart; the
// outer loop then starts a fresh server with the updated options (audio off
// after a crash, new quality preset, etc.). Closing only the control socket
// was not enough — the video read loop blocks on the video socket.
func (s *Session) requestRestart() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.restartWanted = true
	streams := s.streams
	s.mu.Unlock()
	if streams != nil {
		streams.Close()
	}
}

// onBitrateEstimate implements REMB-driven auto downshift.
func (s *Session) onBitrateEstimate(bps int) {
	s.mu.Lock()
	if !s.autoQuality || s.videoPath != "rtc" {
		s.mu.Unlock()
		return
	}
	target := qualityPresets[s.preset].VideoBitRate
	idx := indexOf(presetOrder, s.preset)
	s.mu.Unlock()

	s.rembMu.Lock()
	if float64(bps) < float64(target)*0.6 {
		s.rembLowCount++
	} else {
		s.rembLowCount = 0
	}
	if s.rembLowCount >= 5 && idx > 0 && time.Now().After(s.rembCooldownUntil) {
		newPreset := presetOrder[idx-1]
		s.rembLowCount = 0
		s.rembCooldownUntil = time.Now().Add(15 * time.Second)
		s.rembMu.Unlock()
		s.mu.Lock()
		s.preset = newPreset
		s.mu.Unlock()
		s.log("auto quality: downshifting to %s", newPreset)
		s.sendSignal(map[string]interface{}{"type": "quality", "preset": newPreset, "auto": true})
		s.requestRestart()
		return
	}
	s.rembMu.Unlock()
}

func indexOf(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}
