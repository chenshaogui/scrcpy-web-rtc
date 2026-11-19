package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tiann/scrcpy-webrtc-go/internal/adb"
	"github.com/tiann/scrcpy-webrtc-go/internal/rtc"
	"github.com/tiann/scrcpy-webrtc-go/internal/scrcpy"
)

// Conn is the transport-agnostic message channel a viewer speaks through.
// Both a WebSocket and an encrypted MQTT SignalSocket implement this shape,
// exactly like server.mjs's ws abstraction.
type Conn interface {
	SendText(data string) error
	SendBinary(data []byte) error
	Close(code int, reason string)
	Ready() bool
	SetMessageHandler(fn func(data []byte, binary bool))
	SetCloseHandler(fn func())
}

// quality presets mirror QUALITY_PRESETS in server.mjs.
type qualityPreset struct {
	MaxSize      int
	VideoBitRate int
}

var qualityPresets = map[string]qualityPreset{
	"low":    {800, 1_000_000},
	"medium": {1280, 4_000_000},
	"high":   {1920, 8_000_000},
}
var presetOrder = []string{"low", "medium", "high"}

// Scrcpy server stdout patterns. The audio-encoder crash signature is the one
// native's server.mjs guards against: on redroid / Android 13+ the opus encoder
// thread throws as soon as it starts, which kills the whole scrcpy-server
// process. Without detecting it the session just ends with "video meta: EOF"
// and the page stays black.
var (
	reAudioEncoderCrash = regexp.MustCompile(`Exception on thread Thread\[audio-encoder[^\]]*\]`)
	reVideoEncoderUse   = regexp.MustCompile(`Using video encoder: '([^']+)'`)
	reAudioEncoderUse   = regexp.MustCompile(`Using audio encoder: '([^']+)'`)
)

// Session binds one viewer to one device's scrcpy + WebRTC pipelines.
type Session struct {
	srv  *Server
	conn Conn
	log  func(string, ...interface{})

	serial string
	authed bool

	store *adb.DeviceStore

	mu           sync.Mutex
	rtc          *rtc.Session
	rtcGen       uint32
	control      *adb.Socket
	streams      *scrcpy.Streams // current scrcpy streams, closed on teardown
	transport    *adb.Transport  // current ADB transport, closed on teardown
	videoW       uint16
	videoH       uint16
	videoPath    string // "ws" or "rtc"
	rtcVideo     bool
	preset       string
	autoQuality  bool
	custom       map[string]interface{}
	videoEncoder string
	audioEncoder string
	audioCodec    string
	audioBroken   bool
	audioOffTried bool  // already restarted once with audio off (no restart loop)
	lastConfig    []byte // last H.264 config (SPS/PPS), replayed to fresh RTC peers
	restartWanted bool  // a restart was requested mid-stream; loop re-runs scrcpy
	replaced      bool
	closed        bool

	controlMu      sync.Mutex
	lastControlAt  time.Time
	clipFromDevice string
	clipToDevice   string
	clipSeq        uint64

	rembMu            sync.Mutex
	rembLowCount      int
	rembCooldownUntil time.Time

	shell          *shellSession
	shellTransport *adb.Transport

	// control queue serializes injections (FIFO, one at a time).
	controlChain chan func()

	done chan struct{}
}

// NewSession creates a session for a connection and serial.
func NewSession(srv *Server, conn Conn, serial string, authed bool) *Session {
	s := &Session{
		srv: srv, conn: conn, serial: serial, authed: authed,
		store: srv.store,
		log: func(format string, args ...interface{}) {
			fmt.Printf("[%s] "+format+"\n", append([]interface{}{serial}, args...)...)
		},
		videoPath: "ws", rtcVideo: true, preset: "medium", autoQuality: true,
		videoEncoder: srv.cfg.VideoEncoder, audioEncoder: srv.cfg.AudioEncoder,
		audioCodec:   srv.cfg.AudioCodec,
		controlChain: make(chan func(), 256),
		done:         make(chan struct{}),
	}
	conn.SetMessageHandler(s.onMessage)
	// When the viewer's socket/MQTT session goes away, tear down the session
	// synchronously so the ADB transport is released immediately. Otherwise the
	// device's single-client adbd stays occupied and the next connect is RST.
	conn.SetCloseHandler(func() { s.close() })
	return s
}

func (s *Session) sendSignal(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	_ = s.conn.SendText(string(data))
}

func (s *Session) sendShell(v interface{}) {
	if s.rtc != nil && s.rtc.SendShell(v) {
		return
	}
	s.sendSignal(v)
}

// Run drives the session: ADB transport -> scrcpy stream loop. It blocks until
// the viewer disconnects or an error occurs.
func (s *Session) Run() {
	defer s.close()
	go s.controlWorker()

	if err := s.runStreamLoop(); err != nil {
		s.log("session error: %v", err)
		s.sendSignal(map[string]interface{}{"type": "error", "message": err.Error()})
	}
}

// controlWorker serially drains the injection queue with a minimum gap so
// consecutive taps land in distinct device-millisecond timestamps.
func (s *Session) controlWorker() {
	const minGap = 12 * time.Millisecond
	for {
		select {
		case fn := <-s.controlChain:
			wait := time.Until(s.lastControlAt.Add(minGap))
			if wait > 0 {
				time.Sleep(wait)
			}
			s.lastControlAt = time.Now()
			fn()
		case <-s.done:
			return
		}
	}
}

func (s *Session) enqueueControl(fn func()) {
	select {
	case s.controlChain <- fn:
	default:
		// queue full: drop the oldest-style newest injection to avoid blocking
	}
}

func (s *Session) inject(data []byte) {
	s.mu.Lock()
	c := s.control
	s.mu.Unlock()
	if c == nil {
		return
	}
	_, _ = c.Write(data)
}

// close tears everything down and locks the device when appropriate.
func (s *Session) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	streams := s.streams
	transport := s.transport
	rtcSession := s.rtc
	s.mu.Unlock()
	close(s.done)

	// Release the device FIRST: closing the scrcpy streams unblocks any
	// videoReader.Next() stuck on an ADB socket read, which lets runStreamLoop
	// return and drop the transport. Do it here directly too, because the
	// viewer can disappear while the stream loop is blocked in Next().
	if streams != nil {
		streams.Close()
	}
	if transport != nil {
		_ = transport.Close()
	}
	s.srv.unregisterSession(s.serial, s)
	s.conn.Close(1000, "")
	if rtcSession != nil {
		_ = rtcSession.Close()
	}
	s.stopShell()
	s.log("session closed")
}

var errDisconnected = errors.New("viewer disconnected")

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }

// buildOptions assembles scrcpy options from preset + custom overrides.
func (s *Session) buildOptions() scrcpy.Options {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := qualityPresets[s.preset]
	o := scrcpy.Options{
		MaxSize:           intPtr(q.MaxSize),
		VideoBitRate:      intPtr(q.VideoBitRate),
		Audio:             boolPtr(!s.audioBroken),
		AudioCodec:        s.audioCodec,
		ClipboardAutosync: boolPtr(true),
	}
	if s.videoEncoder != "" {
		o.VideoEncoder = s.videoEncoder
	}
	if s.audioEncoder != "" {
		o.AudioEncoder = s.audioEncoder
	}
	// Apply sanitized custom overrides.
	for k, v := range s.custom {
		switch k {
		case "maxSize":
			if n, ok := num(v); ok {
				o.MaxSize = intPtr(int(n))
			}
		case "videoBitRate":
			if n, ok := num(v); ok {
				o.VideoBitRate = intPtr(int(n))
			}
		case "videoCodec":
			o.VideoCodec, _ = v.(string)
		case "videoEncoder":
			o.VideoEncoder, _ = v.(string)
		case "videoCodecOptions":
			o.VideoCodecOptions, _ = v.(string)
		case "maxFps":
			if n, ok := num(v); ok {
				o.MaxFps = intPtr(int(n))
			}
		case "audio":
			if b, ok := v.(bool); ok {
				o.Audio = boolPtr(b)
			}
		case "audioCodec":
			o.AudioCodec, _ = v.(string)
		case "audioEncoder":
			o.AudioEncoder, _ = v.(string)
		case "audioBitRate":
			if n, ok := num(v); ok {
				o.AudioBitRate = intPtr(int(n))
			}
		case "audioCodecOptions":
			o.AudioCodecOptions, _ = v.(string)
		case "audioSource":
			o.AudioSource, _ = v.(string)
		case "captureOrientation":
			o.CaptureOrientation, _ = v.(string)
		case "angle":
			if n, ok := num(v); ok {
				o.Angle = intPtr(int(n))
			}
		case "screenOffTimeout":
			if n, ok := num(v); ok {
				o.ScreenOffTimeout = intPtr(int(n))
			}
		case "displayId":
			if n, ok := num(v); ok {
				o.DisplayID = intPtr(int(n))
			}
		case "newDisplay":
			o.NewDisplay, _ = v.(string)
		case "crop":
			o.Crop, _ = v.(string)
		case "showTouches":
			if b, ok := v.(bool); ok {
				o.ShowTouches = boolPtr(b)
			}
		case "stayAwake":
			if b, ok := v.(bool); ok {
				o.StayAwake = boolPtr(b)
			}
		case "powerOn":
			if b, ok := v.(bool); ok {
				o.PowerOn = boolPtr(b)
			}
		case "powerOffOnClose":
			if b, ok := v.(bool); ok {
				o.PowerOffOnClose = boolPtr(b)
			}
		case "clipboardAutosync":
			if b, ok := v.(bool); ok {
				o.ClipboardAutosync = boolPtr(b)
			}
		case "control":
			if b, ok := v.(bool); ok {
				o.Control = boolPtr(b)
			}
		case "tunnelForward":
			if b, ok := v.(bool); ok {
				o.TunnelForward = boolPtr(b)
			}
		case "logLevel":
			o.LogLevel, _ = v.(string)
		}
	}
	return o
}

func num(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// runStreamLoop runs one scrcpy server instance per iteration; quality changes
// close the current run and start a fresh one within the same session.
func (s *Session) runStreamLoop() error {
	// Tie the dial to the viewer's lifetime: if the viewer disconnects while
	// the ADB handshake is still pending ("connecting…"), cancel it so the
	// single-client adbd slot is released immediately — otherwise the zombie
	// dial keeps the slot until timeout and the viewer's reconnect fails.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	// Dial a fresh ADB transport for every attempt. redroid's single-client
	// adbd leaves the old transport unusable after a failed scrcpy start
	// (push "sync:" times out, no control connection arrives); reusing it just
	// burns the remaining retries on a dead connection.
	dial := func() (*adb.Transport, error) {
		return s.store.DialSerialContext(ctx, s.serial)
	}

	for {
		select {
		case <-s.done:
			return nil
		default:
		}
		s.mu.Lock()
		s.restartWanted = false
		s.mu.Unlock()
		restartWanted, err := s.runOneStream(dial)
		if err != nil {
			return err
		}
		if !restartWanted {
			return nil
		}
		s.log("restarting stream")
	}
}

// readVideoMetaBounded reads the video header with a deadline. adb.Socket.Read
// has no timeout, so without this a device that accepts the video socket but
// never writes meta (e.g. redroid whose server dies mid-startup) leaves the
// viewer stuck on "连接中…" forever. On timeout we close the streams, which
// unblocks the reader goroutine, and surface an error so the retry loop can
// try again with audio off.
func (s *Session) readVideoMetaBounded(streams *scrcpy.Streams) (scrcpy.VideoMeta, error) {
	type result struct {
		meta scrcpy.VideoMeta
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		m, e := streams.ReadVideoMeta(streams.Video)
		ch <- result{m, e}
	}()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.meta, r.err
	case <-timer.C:
		streams.Close() // unblocks the goroutine's Read
		select {
		case r := <-ch:
			return r.meta, r.err
		case <-time.After(500 * time.Millisecond):
			return scrcpy.VideoMeta{}, fmt.Errorf("video meta timeout")
		}
	case <-s.done:
		streams.Close()
		return scrcpy.VideoMeta{}, errDisconnected
	}
}

// runOneStream returns (restartWanted, error). scrcpy start (push jar + launch
// + meta) is retried up to 3 times, matching server.mjs: redroid briefly
// caches ICMP unreachable after an adbd slot is released, and the device's
// audio encoder can crash the server on the first attempt.
func (s *Session) runOneStream(dial func() (*adb.Transport, error)) (bool, error) {
	var streams *scrcpy.Streams
	var videoMeta scrcpy.VideoMeta
	var transport *adb.Transport
	var opts scrcpy.Options
	var err error
	const maxAttempts = 4
	for attempt := 0; ; attempt++ {
		select {
		case <-s.done:
			return false, errDisconnected
		default:
		}
		transport, err = dial()
		if err == nil {
			opts = s.buildOptions()
			s.log("scrcpy start (%d/%d): %s", attempt+1, maxAttempts, opts.StartCommand())
			streams, err = scrcpy.Start(transport, s.srv.cfg.ServerBin, opts)
		}
		if err == nil {
			// Drain server stdout in parallel: on redroid the audio-encoder
			// crash line is printed just before the process dies, and it is
			// what flips audioBroken for the next attempt.
			go s.pumpServerOutput(streams.ServerOut)
			videoMeta, err = s.readVideoMetaBounded(streams)
			if err != nil {
				s.log("META-DIAG: video socket read failed: %v (SCID=%s, audio=%v)", err, streams.SCID, opts.Audio != nil && *opts.Audio)
			} else {
				s.log("META-DIAG: video meta codec=%d %dx%d", videoMeta.Codec, videoMeta.Width, videoMeta.Height)
			}
		}
		if err == nil {
			break
		}
		if streams != nil {
			streams.Close()
			streams = nil
		}
		if transport != nil {
			_ = transport.Close()
			transport = nil
		}

		s.mu.Lock()
		audioBroken := s.audioBroken
		audioOffTried := s.audioOffTried
		s.mu.Unlock()
		// Two ways to decide audio is the culprit: the device printed the
		// crash signature, or meta failed twice with audio on (redroid often
		// dies before printing anything at all). Either way, don't keep
		// retrying the same broken configuration.
		if !audioOffTried && (audioBroken || attempt >= 1) {
			s.mu.Lock()
			s.audioBroken = true
			s.audioOffTried = true
			s.mu.Unlock()
			s.sendSignal(map[string]interface{}{"type": "audio", "active": false})
			s.log("meta failed with audio on; retrying with audio disabled (%d/%d)",
				attempt+1, maxAttempts)
		}
		if attempt >= maxAttempts-1 {
			return false, fmt.Errorf("scrcpy video meta: %w", err)
		}
		s.log("scrcpy start failed (%v), retrying (%d/%d)...", err, attempt+1, maxAttempts)
		time.Sleep(700 * time.Millisecond)
	}
	s.mu.Lock()
	s.transport = transport
	s.streams = streams
	s.mu.Unlock()
	defer func() {
		streams.Close()
		_ = transport.Close()
		s.mu.Lock()
		if s.streams == streams {
			s.streams = nil
		}
		if s.transport == transport {
			s.transport = nil
		}
		s.mu.Unlock()
	}()

	// The retry loop already re-ran with audio off when needed, so a meta read
	// reaching here is usable — send it instead of restarting again (which
	// would loop forever).
	s.log("stream started codec=%s %dx%d", scrcpy.CodecName(videoMeta.Codec),
		videoMeta.Width, videoMeta.Height)
	s.mu.Lock()
	s.videoW = uint16(videoMeta.Width)
	s.videoH = uint16(videoMeta.Height)
	s.mu.Unlock()

	// Send meta to the viewer.
	s.mu.Lock()
	preset, auto := s.preset, s.autoQuality
	vEnc, aEnc := s.videoEncoder, s.audioEncoder
	audioOK := !s.audioBroken
	s.mu.Unlock()
	s.sendSignal(map[string]interface{}{
		"type": "meta", "codec": scrcpy.CodecName(videoMeta.Codec),
		"width": videoMeta.Width, "height": videoMeta.Height,
		"serverVersion": scrcpy.Version, "preset": preset, "auto": auto,
		"encoder": vEnc, "audioEncoder": aEnc,
		"shell": s.srv.cfg.Shell && s.authed, "audio": audioOK,
	})

	// Pump audio (opus) — send over WebRTC when connected, else tag onto WS.
	if streams.Audio != nil {
		go s.pumpAudio(streams.Audio)
	}
	// Pump clipboard device -> browser.
	go s.pumpClipboard(streams.Control)

	s.mu.Lock()
	s.control = streams.Control
	s.mu.Unlock()

	videoReader := scrcpy.NewMediaReader(streams.Video)
	for {
		select {
		case <-s.done:
			return false, nil
		default:
		}
		pkt, err := videoReader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return s.restartFlag(), nil
			}
			return false, nil // stream ended (e.g. restart or quality change)
		}
		s.sendVideoPacket(pkt)
	}
}

func (s *Session) restartFlag() bool {
	// A clean stream end is a restart only when something explicitly asked for
	// one (quality/encoder change, or the audio-encoder crash guard below).
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restartWanted
}

// sendVideoPacket forwards a video packet to WebRTC and/or the WebSocket.
func (s *Session) sendVideoPacket(pkt *scrcpy.MediaPacket) {
	if pkt.Config {
		// Keep the latest SPS/PPS so a WebRTC peer created after the config
		// frame can still get a decodable keyframe (server.mjs's lastConfig).
		s.mu.Lock()
		s.lastConfig = append([]byte(nil), pkt.Data...)
		s.mu.Unlock()
	}
	s.mu.Lock()
	rtcSession, rtcVideo, videoPath := s.rtc, s.rtcVideo, s.videoPath
	s.mu.Unlock()

	if rtcSession != nil && rtcVideo {
		func() {
			defer func() { _ = recover() }()
			rtcSession.SendVideoPacket(pkt.Config, pkt.Keyframe, pkt.PTS, pkt.Data)
		}()
	}
	if videoPath == "rtc" && !pkt.Config {
		return
	}
	// Frame the packet for the WS video decoder:
	// byte0: 0=config, 1=data; byte1: keyframe; bytes2-9: pts (u64 BE —
	// the page reads it with DataView.getBigUint64's default big-endian,
	// same as server.mjs's setBigUint64)
	buf := make([]byte, 10+len(pkt.Data))
	if pkt.Config {
		buf[0] = 0
	} else {
		buf[0] = 1
		if pkt.Keyframe {
			buf[1] = 1
		}
		binary.BigEndian.PutUint64(buf[2:10], pkt.PTS)
	}
	copy(buf[10:], pkt.Data)
	_ = s.conn.SendBinary(buf)
}

func (s *Session) pumpAudio(sock *adb.Socket) {
	reader := scrcpy.NewMediaReader(sock)
	for {
		select {
		case <-s.done:
			return
		default:
		}
		pkt, err := reader.Next()
		if err != nil {
			return
		}
		s.mu.Lock()
		rtcSession := s.rtc
		s.mu.Unlock()
		if rtcSession != nil && rtcSession.Connected() {
			rtcSession.SendAudioPacket(pkt.Config, pkt.PTS, pkt.Data)
		} else if !pkt.Config {
			// Tag opus frames (0x02) onto the WS for the WS decoder.
			buf := make([]byte, 10+len(pkt.Data))
			buf[0] = 2
			binary.BigEndian.PutUint64(buf[2:10], pkt.PTS)
			copy(buf[10:], pkt.Data)
			_ = s.conn.SendBinary(buf)
		}
	}
}

func (s *Session) pumpClipboard(control *adb.Socket) {
	// Clipboard device messages arrive as ID 0 frames on the control socket;
	// this is handled minimally here. The browser-to-device direction is the
	// common path and is implemented in onMessage.
	// A full device-message parser would multiplex with control writes; omitted
	// to keep the control socket single-writer. See README for details.
}

func (s *Session) pumpServerOutput(sock *adb.Socket) {
	if sock == nil {
		return
	}
	buf := make([]byte, 4096)
	lineBuf := ""
	for {
		n, err := sock.Read(buf)
		if n > 0 {
			lineBuf += string(buf[:n])
			for {
				idx := strings.IndexByte(lineBuf, '\n')
				if idx < 0 {
					break
				}
				line := strings.TrimRight(lineBuf[:idx], "\r")
				lineBuf = lineBuf[idx+1:]
				s.handleServerLine(line)
			}
		}
		if err != nil {
			// The crash line often arrives without a trailing newline right
			// before the server dies — don't drop it.
			if strings.TrimSpace(lineBuf) != "" {
				s.handleServerLine(strings.TrimRight(lineBuf, "\r"))
			}
			return
		}
	}
}

func (s *Session) handleServerLine(line string) {
	s.log("server: %s", line)

	// Surface the encoder the device actually opened, same as server.mjs, so
	// an ignored/unusable selection is visible in the page.
	if m := reVideoEncoderUse.FindStringSubmatch(line); m != nil {
		s.sendSignal(map[string]interface{}{
			"type": "encoder", "name": m[1], "active": true,
		})
	}
	if m := reAudioEncoderUse.FindStringSubmatch(line); m != nil {
		s.sendSignal(map[string]interface{}{
			"type": "audio-encoder", "name": m[1],
			"codec": codecFromEncoderName(m[1]), "active": true,
		})
	}

	// redroid / Android 13+ ships a MediaCodec whose opus encoder thread
	// throws as soon as it starts, killing scrcpy-server before it writes the
	// video meta. Without this guard the viewer just sees a black screen and
	// "video meta: EOF". Disable audio for the rest of the connection and
	// restart the stream with audio:false — exactly like server.mjs.
	if reAudioEncoderCrash.MatchString(line) {
		s.mu.Lock()
		alreadyBroken := s.audioBroken
		s.audioBroken = true
		s.mu.Unlock()
		if !alreadyBroken {
			s.log("device audio encoder crashed — disabling audio and restarting")
			s.sendSignal(map[string]interface{}{"type": "audio", "active": false})
			s.requestRestart()
		}
	}
}

// WSConn adapts a gorilla WebSocket to the Conn interface.
type WSConn struct {
	ws         *websocket.Conn
	mu         sync.Mutex
	handler    func([]byte, bool)
	closeFn    func()
	closeFired bool
	closed     bool
}

func NewWSConn(ws *websocket.Conn) *WSConn {
	c := &WSConn{ws: ws}
	go func() {
		defer c.fireClose()
		for {
			typ, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			c.mu.Lock()
			fn := c.handler
			c.mu.Unlock()
			if fn != nil {
				fn(data, typ == websocket.BinaryMessage)
			}
		}
	}()
	return c
}

func (c *WSConn) fireClose() {
	c.mu.Lock()
	if c.closeFired {
		c.mu.Unlock()
		return
	}
	c.closeFired = true
	c.closed = true
	fn := c.closeFn
	c.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (c *WSConn) SendText(data string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("closed")
	}
	return c.ws.WriteMessage(websocket.TextMessage, []byte(data))
}

func (c *WSConn) SendBinary(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("closed")
	}
	return c.ws.WriteMessage(websocket.BinaryMessage, data)
}

func (c *WSConn) Close(code int, reason string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	_ = c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
	_ = c.ws.Close()
	c.fireClose()
}

func (c *WSConn) Ready() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed
}

func (c *WSConn) SetMessageHandler(fn func(data []byte, binary bool)) {
	c.mu.Lock()
	c.handler = fn
	c.mu.Unlock()
}

func (c *WSConn) SetCloseHandler(fn func()) {
	c.mu.Lock()
	c.closeFn = fn
	c.mu.Unlock()
}
