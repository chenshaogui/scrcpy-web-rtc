package scrcpy

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestInjectTouchLayout(t *testing.T) {
	data := InjectTouch(ActionDown, 100, 200, 1080, 1920, 1, 1)
	if len(data) != 32 {
		t.Fatalf("InjectTouch length = %d, want 32", len(data))
	}
	if data[0] != CtrlInjectTouch || data[1] != ActionDown {
		t.Errorf("type/action = %d/%d", data[0], data[1])
	}
	if got := binary.BigEndian.Uint64(data[2:10]); got != PointerFinger {
		t.Errorf("pointerId = %#x, want %#x", got, PointerFinger)
	}
	if got := binary.BigEndian.Uint32(data[10:14]); got != 100 {
		t.Errorf("pointerX = %d", got)
	}
	if got := binary.BigEndian.Uint32(data[14:18]); got != 200 {
		t.Errorf("pointerY = %d", got)
	}
	if got := binary.BigEndian.Uint16(data[18:20]); got != 1080 {
		t.Errorf("videoWidth = %d", got)
	}
	if got := binary.BigEndian.Uint16(data[20:22]); got != 1920 {
		t.Errorf("videoHeight = %d", got)
	}
	if got := binary.BigEndian.Uint16(data[22:24]); got != 0xffff {
		t.Errorf("pressure = %#x, want 0xffff", got)
	}
	if got := binary.BigEndian.Uint32(data[28:32]); got != 1 {
		t.Errorf("buttons = %d", got)
	}
}

func TestInjectKeyCodeLayout(t *testing.T) {
	data := InjectKeyCode(0, 66, 0, 0) // Enter
	if len(data) != 14 {
		t.Fatalf("InjectKeyCode length = %d, want 14", len(data))
	}
	if data[0] != CtrlInjectKeyCode || data[1] != 0 {
		t.Errorf("type/action = %d/%d", data[0], data[1])
	}
	if got := binary.BigEndian.Uint32(data[2:6]); got != 66 {
		t.Errorf("keyCode = %d", got)
	}
}

func TestInjectPowerKeyLayout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action uint32
	}{
		{name: "down", action: 0},
		{name: "up", action: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := InjectKeyCode(tc.action, 26, 0, 0)
			if got := binary.BigEndian.Uint32(data[2:6]); got != 26 {
				t.Fatalf("keyCode = %d, want KEYCODE_POWER (26)", got)
			}
			if got := uint32(data[1]); got != tc.action {
				t.Fatalf("action = %d, want %d", got, tc.action)
			}
		})
	}
}

func TestInjectTextLayout(t *testing.T) {
	data := InjectText("abc")
	// type(1) + len(4) + 3 bytes
	if len(data) != 8 {
		t.Fatalf("InjectText length = %d, want 8", len(data))
	}
	if data[0] != CtrlInjectText {
		t.Errorf("type = %d", data[0])
	}
	if got := binary.BigEndian.Uint32(data[1:5]); got != 3 {
		t.Errorf("text length = %d", got)
	}
	if string(data[5:]) != "abc" {
		t.Errorf("text = %q", string(data[5:]))
	}
}

func TestSetClipboardLayout(t *testing.T) {
	data := SetClipboard(42, true, "hi")
	// type(1) + seq(8) + paste(1) + len(4) + 2
	if len(data) != 16 {
		t.Fatalf("SetClipboard length = %d, want 16", len(data))
	}
	if data[0] != CtrlSetClipboard {
		t.Errorf("type = %d", data[0])
	}
	if got := binary.BigEndian.Uint64(data[1:9]); got != 42 {
		t.Errorf("sequence = %d", got)
	}
	if data[9] != 1 {
		t.Errorf("paste = %d", data[9])
	}
	if got := binary.BigEndian.Uint32(data[10:14]); got != 2 {
		t.Errorf("content length = %d", got)
	}
}

func TestOptionsSerializeOmitsDefaults(t *testing.T) {
	o := Options{}
	args := o.Serialize()
	if len(args) != 0 {
		t.Errorf("default options should serialize to nothing, got %v", args)
	}

	bi, bb := 4_000_000, false
	o = Options{VideoCodec: "h265", VideoBitRate: &bi, Audio: &bb}
	args = o.Serialize()
	joined := stringsJoin(args, " ")
	for _, want := range []string{"video_codec=h265", "video_bit_rate=4000000", "audio=false"} {
		if !contains(args, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	for _, a := range args {
		if a == "video_codec=h264" || a == "audio=true" {
			t.Errorf("default value should be omitted: %s", a)
		}
	}
}

func TestOptionsSerializeListEncoders(t *testing.T) {
	audio, control, list := false, false, true
	args := (Options{
		Audio:        &audio,
		Control:      &control,
		ListEncoders: &list,
	}).Serialize()
	for _, want := range []string{"audio=false", "control=false", "list_encoders=true"} {
		if !contains(args, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
}

func TestParseEncoderLine(t *testing.T) {
	tests := []struct {
		line string
		want Encoder
		ok   bool
	}{
		{
			line: "    --video-codec=h264 --video-encoder=c2.android.avc.encoder (sw)",
			want: Encoder{Type: "video", Codec: "h264", Name: "c2.android.avc.encoder", HardwareType: "software"},
			ok:   true,
		},
		{
			line: "    --audio-codec=opus --audio-encoder=c2.android.opus.encoder (hw) [vendor] (alias for c2.android.opus)",
			want: Encoder{Type: "audio", Codec: "opus", Name: "c2.android.opus.encoder", HardwareType: "hardware", Vendor: true, AliasFor: "c2.android.opus"},
			ok:   true,
		},
		{
			line: "    --video-codec=h264 --video-encoder=OMX.example.encoder",
			want: Encoder{Type: "video", Codec: "h264", Name: "OMX.example.encoder"},
			ok:   true,
		},
		{line: "not an encoder line", ok: false},
		{line: "    --video-codec=h264 --audio-encoder=wrong-type", ok: false},
	}
	for _, tt := range tests {
		got, ok := ParseEncoderLine(tt.line)
		if ok != tt.ok {
			t.Errorf("ParseEncoderLine(%q) ok=%v, want %v", tt.line, ok, tt.ok)
			continue
		}
		if ok && got != tt.want {
			t.Errorf("ParseEncoderLine(%q) = %+v, want %+v", tt.line, got, tt.want)
		}
	}
}

func TestMediaPacketParsing(t *testing.T) {
	// Build a stream with a config packet (bit63) and a keyframe data packet (bit62).
	var buf bytes.Buffer
	writeMedia := func(pts uint64, data []byte) {
		binary.Write(&buf, binary.BigEndian, pts)
		binary.Write(&buf, binary.BigEndian, uint32(len(data)))
		buf.Write(data)
	}
	writeMedia(uint64(1)<<63, []byte{0x67, 0x42})         // config
	writeMedia((uint64(1)<<62)|12345, []byte{0x65, 0x88}) // keyframe

	r := NewMediaReader(&buf)
	p1, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !p1.Config || !bytes.Equal(p1.Data, []byte{0x67, 0x42}) {
		t.Errorf("config packet wrong: %+v", p1)
	}
	p2, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !p2.Keyframe || p2.PTS != 12345 || !bytes.Equal(p2.Data, []byte{0x65, 0x88}) {
		t.Errorf("keyframe packet wrong: %+v", p2)
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func stringsJoin(list []string, sep string) string {
	var b bytes.Buffer
	for i, s := range list {
		if i > 0 {
			b.WriteString(sep)
		}
		b.WriteString(s)
	}
	return b.String()
}
