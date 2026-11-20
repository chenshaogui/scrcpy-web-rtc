// Command scrcpy-webrtc is a full Go reimplementation of the scrcpy-webrtc
// server: ADB over TCP, scrcpy protocol, WebRTC (pion), MQTT signaling and the
// HTTP/WebSocket API. No Node.js runtime is required.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/tiann/scrcpy-webrtc-go/internal/server"
)

func loadServerBin(exeDir string) []byte {
	// The jar is shipped next to the binary (or in the working directory).
	for _, p := range []string{
		filepath.Join(exeDir, "server.bin"),
		"server.bin",
		filepath.Join(exeDir, "scrcpy-server.bin"),
	} {
		if data, err := os.ReadFile(p); err == nil && len(data) > 0 {
			return data
		}
	}
	fmt.Fprintln(os.Stderr, "scrcpy server binary not found; place server.bin next to the binary")
	fmt.Fprintln(os.Stderr, "  download: https://github.com/Genymobile/scrcpy/releases/download/v3.1/scrcpy-server-v3.1")
	os.Exit(1)
	return nil
}

func publicDir(exeDir string) string {
	for _, p := range []string{
		filepath.Join(exeDir, "web"),
		filepath.Join(exeDir, "public"),
		"web",
		"public",
		filepath.Join("..", "..", "public"),
	} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return "public"
}

func main() {
	exe, err := os.Executable()
	if err != nil {
		exe = "scrcpy-webrtc"
	}
	exeDir := filepath.Dir(exe)

	serverBin := loadServerBin(exeDir)
	cfg, err := server.Parse(serverBin, publicDir(exeDir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}

	srv := server.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if cfg.SignalEnabled {
		if err := srv.StartSignal(); err != nil {
			fmt.Fprintf(os.Stderr, "serverless signaling unavailable (%v)\n", err)
			fmt.Fprintf(os.Stderr, "local access still works: http://localhost:%d/\n", cfg.Port)
		}
	}

	fmt.Printf("scrcpy-webrtc (Go) listening on http://%s:%d (scrcpy server v3.1)\n", cfg.Host, cfg.Port)
	if cfg.Token != "" {
		fmt.Printf("  local: http://localhost:%d/?token=%s\n", cfg.Port, cfg.Token)
	} else {
		fmt.Printf("  local: http://localhost:%d/\n", cfg.Port)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	if err := srv.Serve(ctx); err != nil && err != context.Canceled {
		fmt.Fprintln(os.Stderr, "server error:", err)
		os.Exit(1)
	}
}
