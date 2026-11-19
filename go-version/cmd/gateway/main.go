// Command gateway is the Go replacement for fnos-app-native/proxy.cjs.
// It accepts the same environment variables exported by cmd/main:
//
//	GATEWAY_SOCKET, GATEWAY_PREFIX, INNER_PORT, WEB_DIR, LOG_FILE
package main

import (
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tiann/scrcpy-webrtc-go/internal/gateway"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	socket := flag.String("socket", env("GATEWAY_SOCKET", ""), "unix socket path (GATEWAY_SOCKET)")
	prefix := flag.String("prefix", env("GATEWAY_PREFIX", "/app/scrcpy-webrtc"), "gateway path prefix (GATEWAY_PREFIX)")
	target := flag.String("target", env("GATEWAY_TARGET", "http://127.0.0.1:"+env("INNER_PORT", "18010")), "inner HTTP server (GATEWAY_TARGET / INNER_PORT)")
	webDir := flag.String("web-dir", env("WEB_DIR", ""), "static page directory (WEB_DIR)")
	logFile := flag.String("log-file", env("LOG_FILE", ""), "log file (LOG_FILE)")
	flag.Parse()

	if *socket == "" {
		log.Fatal("gateway: -socket or GATEWAY_SOCKET is required")
	}

	p, err := gateway.New(gateway.Config{
		Socket:  *socket,
		Prefix:  *prefix,
		Target:  *target,
		WebDir:  *webDir,
		LogFile: *logFile,
	})
	if err != nil {
		log.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- p.ListenAndServe() }()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-stop:
		log.Printf("received %s, shutting down", sig)
		_ = p.Close()
		// Give in-flight requests a moment to finish.
		time.Sleep(200 * time.Millisecond)
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}
}
