// Package server implements the HTTP/WebSocket API and session management,
// replacing server.mjs end to end.
package server

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Host           string
	Port           int
	ADBDirect      bool
	VideoEncoder   string
	AudioEncoder   string
	Shell          bool
	Token          string
	ExplicitToken  string
	ViewNeedsToken bool
	PageURL        string
	PublicDir      string
	NoQR           bool

	VideoBitRate int
	AudioCodec   string

	TURNTarget string
	TURNAuth   string

	SignalEnabled bool
	SignalBrokers []string
	SignalKeyFile string

	VarDir     string
	ServerBin  []byte
	ServerPath string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func truthy(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func hasArg(name string) bool {
	for _, a := range os.Args[1:] {
		if a == name {
			return true
		}
	}
	return false
}

// Parse parses flags and environment into a Config. serverBin is the bundled
// scrcpy server jar (loaded by the caller).
func Parse(serverBin []byte, publicDir string) (*Config, error) {
	fs := flag.NewFlagSet("scrcpy-webrtc", flag.ContinueOnError)
	host := fs.String("host", env("HOST", "0.0.0.0"), "HTTP/WebSocket listen host")
	port := fs.Int("port", atoi(env("PORT", "8010")), "HTTP/WebSocket listen port")
	videoEncoder := fs.String("video-encoder", env("VIDEO_ENCODER", ""), "pin video encoder")
	audioEncoder := fs.String("audio-encoder", env("AUDIO_ENCODER", ""), "pin audio encoder")
	shell := fs.Bool("shell", false, "enable device shell")
	token := fs.String("token", env("SCRCPY_WEBRTC_TOKEN", ""), "require token")
	page := fs.String("page", env("SCRCPY_WEBRTC_PAGE", "https://weishu.me/scrcpy-webrtc/"), "frontend URL")
	noPage := fs.Bool("no-page", false, "serve bundled page")
	noQR := fs.Bool("no-qr", false, "don't print QR code")
	turn := fs.String("turn", env("TURN_URL", ""), "TURN target")
	turnAuth := fs.String("turn-auth", env("TURN_AUTH", ""), "TURN auth user:pass")
	signal := fs.Bool("signal", false, "force signaling on")
	noSignal := fs.Bool("no-signal", false, "disable signaling")
	signalBroker := fs.String("signal-broker", env("SCRCPY_WEBRTC_SIGNAL_BROKERS", ""), "comma-separated MQTT brokers")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return nil, err
	}

	explicitToken := *token
	tok := explicitToken
	if tok == "" && *shell {
		tok = randomHex(16)
	}

	pageURL := *page
	if *noPage {
		pageURL = ""
	}

	signalEnabled := *signal || (!*noSignal && pageURL != "")

	var brokers []string
	if *signalBroker != "" {
		for _, s := range strings.Split(*signalBroker, ",") {
			if s = strings.TrimSpace(s); s != "" {
				brokers = append(brokers, s)
			}
		}
	}

	varDir := env("TRIM_PKGVAR", "")
	if varDir == "" {
		home, _ := os.UserHomeDir()
		varDir = filepath.Join(home, ".config", "scrcpy-webrtc")
	}
	keyFile := filepath.Join(varDir, "signal-key")

	audioCodec := codecFromEncoderName(*audioEncoder)
	if audioCodec == "" {
		audioCodec = "opus"
	}

	return &Config{
		Host:           *host,
		Port:           *port,
		ADBDirect:      truthy(env("ADB_DIRECT", "")) || hasArg("--direct-adb"),
		VideoEncoder:   *videoEncoder,
		AudioEncoder:   *audioEncoder,
		Shell:          *shell,
		Token:          tok,
		ExplicitToken:  explicitToken,
		ViewNeedsToken: explicitToken != "",
		PageURL:        pageURL,
		PublicDir:      publicDir,
		NoQR:           *noQR,
		VideoBitRate:   8_000_000,
		AudioCodec:     audioCodec,
		TURNTarget:     *turn,
		TURNAuth:       *turnAuth,
		SignalEnabled:  signalEnabled,
		SignalBrokers:  brokers,
		SignalKeyFile:  keyFile,
		VarDir:         varDir,
		ServerBin:      serverBin,
		ServerPath:     "/data/local/tmp/scrcpy-webrtc-server.jar",
	}, nil
}

func codecFromEncoderName(name string) string {
	parts := strings.Split(name, ".")
	for _, c := range []string{"raw", "opus", "aac"} {
		for _, p := range parts {
			if p == c {
				return c
			}
		}
	}
	return ""
}

func atoi(s string) int {
	var n int
	_, _ = fmt.Sscanf(s, "%d", &n)
	return n
}
