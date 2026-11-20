// Package scrcpy implements the scrcpy v3.1 protocol: option serialization,
// server startup, media packet parsing and control message encoding.
package scrcpy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	Version     = "3.1"
	ServerPath  = "/data/local/tmp/scrcpy-webrtc-server.jar"
	ServerClass = "com.genymobile.scrcpy.Server"
)

var encoderLinePattern = regexp.MustCompile(
	`^\s+--(video|audio)-codec=(\S+)\s+--(video|audio)-encoder=(\S+)(?:\s*\((sw|hw|hybrid)\))?(?:\s*(\[vendor\]))?(?:\s*\(alias for (\S+)\))?$`,
)

type Encoder struct {
	Type         string `json:"type"`
	Name         string `json:"name"`
	Codec        string `json:"codec,omitempty"`
	HardwareType string `json:"hardwareType,omitempty"`
	Vendor       bool   `json:"vendor,omitempty"`
	AliasFor     string `json:"aliasFor,omitempty"`
}

func ParseEncoderLine(line string) (Encoder, bool) {
	m := encoderLinePattern.FindStringSubmatch(line)
	if m == nil || m[1] != m[3] {
		return Encoder{}, false
	}
	hardwareType := ""
	switch m[5] {
	case "sw":
		hardwareType = "software"
	case "hw":
		hardwareType = "hardware"
	case "hybrid":
		hardwareType = "hybrid"
	}
	return Encoder{
		Type:         m[1],
		Codec:        m[2],
		Name:         m[4],
		HardwareType: hardwareType,
		Vendor:       m[6] != "",
		AliasFor:     m[7],
	}, true
}

// Options mirrors the subset of AdbScrcpyOptions3_1 the app exposes. A nil
// pointer means "use the device default" (option omitted from the command line).
type Options struct {
	SCID                string
	ServerVersion       string // empty means Version (3.1); override for diagnostics
	LogLevel            string
	MaxSize             *int
	MaxFps              *int
	TunnelForward       *bool
	Crop                string
	Control             *bool
	DisplayID           *int
	ShowTouches         *bool
	StayAwake           *bool
	PowerOffOnClose     *bool
	ClipboardAutosync   *bool
	PowerOn             *bool
	VideoCodec          string
	VideoBitRate        *int
	VideoCodecOptions   string
	VideoEncoder        string
	Audio               *bool
	AudioCodec          string
	AudioBitRate        *int
	AudioCodecOptions   string
	AudioEncoder        string
	ListEncoders        *bool
	SendCodecMeta       *bool
	Video               *bool
	AudioSource         string
	CaptureOrientation  string
	Angle               *int
	ScreenOffTimeout    *int
	NewDisplay          string
	VDSystemDecorations *bool
	VDDestroyContent    *bool
}

// Defaults are the scrcpy 3.1 server defaults; an option equal to its default
// is omitted, exactly like the upstream serializer.
var defaults = map[string]string{
	"log_level":             "debug",
	"max_size":              "0",
	"max_fps":               "0",
	"tunnel_forward":        "false",
	"send_frame_meta":       "true",
	"control":               "true",
	"display_id":            "0",
	"show_touches":          "false",
	"stay_awake":            "false",
	"power_off_on_close":    "false",
	"clipboard_autosync":    "true",
	"downsize_on_error":     "true",
	"send_device_meta":      "true",
	"send_dummy_byte":       "true",
	"cleanup":               "true",
	"power_on":              "true",
	"video_codec":           "h264",
	"video_bit_rate":        "8000000",
	"audio":                 "true",
	"audio_codec":           "opus",
	"audio_bit_rate":        "128000",
	"list_encoders":         "false",
	"list_displays":         "false",
	"send_codec_meta":       "true",
	"video":                 "true",
	"audio_source":          "output",
	"video_source":          "display",
	"camera_high_speed":     "false",
	"list_cameras":          "false",
	"list_camera_sizes":     "false",
	"audio_dup":             "false",
	"angle":                 "0",
	"list_apps":             "false",
	"vd_system_decorations": "true",
	"vd_destroy_content":    "false",
}

// ordered list of (command-line key, current value, whether set). Order must
// match the upstream Defaults insertion order for byte-identical commands.
func (o Options) pairs() []struct {
	key, val string
	set      bool
} {
	type kv = struct {
		key, val string
		set      bool
	}
	boolVal := func(p *bool) (string, bool) {
		if p == nil {
			return "", false
		}
		return strconv.FormatBool(*p), true
	}
	intVal := func(p *int) (string, bool) {
		if p == nil {
			return "", false
		}
		return strconv.Itoa(*p), true
	}
	r := make([]kv, 0, 30)
	addStr := func(key, val string) { r = append(r, kv{key, val, val != ""}) }
	addInt := func(key string, p *int) {
		if v, ok := intVal(p); ok {
			r = append(r, kv{key, v, true})
		} else {
			r = append(r, kv{key, "", false})
		}
	}
	addBool := func(key string, p *bool) {
		if v, ok := boolVal(p); ok {
			r = append(r, kv{key, v, true})
		} else {
			r = append(r, kv{key, "", false})
		}
	}

	addStr("log_level", o.LogLevel)
	addInt("max_size", o.MaxSize)
	addInt("max_fps", o.MaxFps)
	addBool("tunnel_forward", o.TunnelForward)
	addStr("crop", o.Crop)
	addBool("control", o.Control)
	addInt("display_id", o.DisplayID)
	addBool("show_touches", o.ShowTouches)
	addBool("stay_awake", o.StayAwake)
	addBool("power_off_on_close", o.PowerOffOnClose)
	addBool("clipboard_autosync", o.ClipboardAutosync)
	addBool("power_on", o.PowerOn)
	addStr("scid", o.SCID)
	addStr("video_codec", o.VideoCodec)
	addInt("video_bit_rate", o.VideoBitRate)
	addStr("video_codec_options", o.VideoCodecOptions)
	addStr("video_encoder", o.VideoEncoder)
	addBool("audio", o.Audio)
	addStr("audio_codec", o.AudioCodec)
	addInt("audio_bit_rate", o.AudioBitRate)
	addStr("audio_codec_options", o.AudioCodecOptions)
	addStr("audio_encoder", o.AudioEncoder)
	addBool("list_encoders", o.ListEncoders)
	addBool("send_codec_meta", o.SendCodecMeta)
	addBool("video", o.Video)
	addStr("audio_source", o.AudioSource)
	addStr("capture_orientation", o.CaptureOrientation)
	addInt("angle", o.Angle)
	addInt("screen_off_timeout", o.ScreenOffTimeout)
	addStr("new_display", o.NewDisplay)
	addBool("vd_system_decorations", o.VDSystemDecorations)
	addBool("vd_destroy_content", o.VDDestroyContent)
	return r
}

// Serialize returns the key=value args, omitting options at their defaults.
func (o Options) Serialize() []string {
	var out []string
	for _, p := range o.pairs() {
		if !p.set {
			continue
		}
		if def, ok := defaults[p.key]; ok && def == p.val {
			continue
		}
		out = append(out, fmt.Sprintf("%s=%s", p.key, p.val))
	}
	return out
}

// StartCommand returns the full argv passed to adb `exec:`.
func (o Options) StartCommand() string {
	version := o.ServerVersion
	if version == "" {
		version = Version
	}
	parts := []string{
		"CLASSPATH=" + ServerPath,
		"app_process", "/", ServerClass, version,
	}
	parts = append(parts, o.Serialize()...)
	return strings.Join(parts, " ")
}

// SocketName is the abstract unix socket the device connects back to.
func SocketName(scid string) string {
	name := "localabstract:scrcpy"
	if scid != "" {
		name += "_" + padLeft(scid, '0', 8)
	}
	return name
}

func padLeft(s string, pad byte, n int) string {
	if len(s) >= n {
		return s
	}
	return strings.Repeat(string(pad), n-len(s)) + s
}
