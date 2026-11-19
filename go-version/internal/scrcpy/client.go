package scrcpy

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/tiann/scrcpy-webrtc-go/internal/adb"
)

// Streams holds the three ADB sockets scrcpy-server connects back through.
type Streams struct {
	Video     *adb.Socket
	Audio     *adb.Socket
	Control   *adb.Socket
	ServerOut *adb.Socket // the exec: process socket (server stdout/stderr)
	SCID      string
	t         *adb.Transport
}

// VideoMeta is the parsed video stream header.
type VideoMeta struct {
	Codec  uint32
	Width  uint32
	Height uint32
}

func randomSCID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	v := binary.LittleEndian.Uint32(b[:]) & 0x7fffffff
	return strconv.FormatUint(uint64(v), 16)
}

// Start pushes the server jar, sets up reverse forwarding, launches the server
// and accepts the video/audio/control connections in that order — matching
// @yume-chan/adb-scrcpy's AdbScrcpyClient.start().
func Start(t *adb.Transport, serverBin []byte, o Options) (*Streams, error) {
	o.SCID = randomSCID()
	deviceAddr := SocketName(o.SCID)

	// The server jar self-deletes after each run, so always re-push.
	if err := t.PushFile(serverBin, ServerPath, time.Now().Unix()); err != nil {
		return nil, fmt.Errorf("scrcpy: push server: %w", err)
	}

	forward := o.TunnelForward != nil && *o.TunnelForward
	if !forward {
		// Reverse mode (default): install a handler so the device's server can
		// connect back to our ADB transport over the abstract socket.
		if _, err := t.ReverseForward(deviceAddr); err != nil {
			return nil, fmt.Errorf("scrcpy: reverse: %w", err)
		}
	}

	// Launch the server process. Its stdout/stderr stream reports encoder info.
	serverOut, err := t.OpenSocket("exec:" + o.StartCommand())
	if err != nil {
		return nil, fmt.Errorf("scrcpy: start server: %w", err)
	}

	// The server only opens the streams that are enabled. In particular with
	// audio=false it opens just video (+control), NOT a dummy audio socket.
	// Accepting an extra socket mislabels control as audio and leaves the real
	// control accept to time out ("no control connection from device"). Mirror
	// @yume-chan's getStreams(), which gates each stream on its option.
	wantAudio := o.Audio == nil || *o.Audio
	wantControl := o.Control == nil || *o.Control

	var video, audio, control *adb.Socket
	if forward {
		// Forward mode: host dials the device abstract socket; retry up to 100x.
		for i := 0; i < 100 && video == nil; i++ {
			video, _ = t.OpenSocket(deviceAddr)
			if video == nil {
				time.Sleep(100 * time.Millisecond)
			}
		}
		if video == nil {
			serverOut.Close()
			return nil, errors.New("scrcpy: cannot connect to server (forward mode)")
		}
		// First stream carries a one-byte dummy byte that must be consumed.
		var dummy [1]byte
		_, _ = io.ReadFull(video, dummy[:])
		if wantAudio {
			audio, _ = t.OpenSocket(deviceAddr)
		}
		if wantControl {
			control, _ = t.OpenSocket(deviceAddr)
		}
	} else {
		// Reverse mode: the device opens the enabled sockets in order:
		// video, then audio (if enabled), then control (if enabled).
		video, _ = t.AcceptReverse(15*time.Second, deviceAddr)
		if video == nil {
			serverOut.Close()
			return nil, errors.New("scrcpy: no video connection from device")
		}
		if wantAudio {
			audio, _ = t.AcceptReverse(5*time.Second, deviceAddr)
		}
		if wantControl {
			control, _ = t.AcceptReverse(5*time.Second, deviceAddr)
		}
	}
	if control == nil {
		serverOut.Close()
		return nil, errors.New("scrcpy: no control connection from device")
	}
	return &Streams{
		Video: video, Audio: audio, Control: control,
		ServerOut: serverOut, SCID: o.SCID, t: t,
	}, nil
}

// ReadVideoMeta parses the head of the video stream: optional 64-byte device
// name (sendDeviceMeta) then big-endian u32 codec, width, height (sendCodecMeta).
func (s *Streams) ReadVideoMeta(r io.Reader) (VideoMeta, error) {
	var name [64]byte
	if n, err := io.ReadFull(r, name[:]); err != nil {
		return VideoMeta{}, fmt.Errorf("read device name (%d/64 bytes): %w", n, err)
	}
	var hdr [12]byte
	if n, err := io.ReadFull(r, hdr[:]); err != nil {
		return VideoMeta{}, fmt.Errorf("read codec meta (%d/12 bytes): %w", n, err)
	}
	return VideoMeta{
		Codec:  binary.BigEndian.Uint32(hdr[0:4]),
		Width:  binary.BigEndian.Uint32(hdr[4:8]),
		Height: binary.BigEndian.Uint32(hdr[8:12]),
	}, nil
}

// Close tears down all streams.
func (s *Streams) Close() {
	if s.Video != nil {
		_ = s.Video.Close()
	}
	if s.Audio != nil {
		_ = s.Audio.Close()
	}
	if s.Control != nil {
		_ = s.Control.Close()
	}
	if s.ServerOut != nil {
		_ = s.ServerOut.Close()
	}
}
