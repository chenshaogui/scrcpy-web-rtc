// Command diag reproduces, layer by layer, the exact ADB connection sequence
// used by the Go scrcpy-webrtc server, so a "can't connect / no video" report
// can be pinned to one concrete layer instead of being guessed from screenshots.
//
// Cross-compile for a fnOS NAS:
//
//	GOOS=linux GOARCH=arm64 go build -o dist/diag-arm64 ./cmd/diag
//	GOOS=linux GOARCH=amd64 go build -o dist/diag-amd64 ./cmd/diag
//
// Run on the NAS (stop the native version and the fpk first — redroid adbd is
// single-client):
//
//	./diag-arm64 -addr 172.16.2.95:5555
//	./diag-arm64 -addr 172.16.2.95:5555 -full -server ./server.bin
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tiann/scrcpy-webrtc-go/internal/adb"
	"github.com/tiann/scrcpy-webrtc-go/internal/scrcpy"
)

func mark(ok bool) string {
	if ok {
		return "[ OK ]"
	}
	return "[FAIL]"
}

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }

func main() {
	addrFlag := flag.String("addr", "172.16.2.95:5555", "ADB target host:port")
	keyFlag := flag.String("key", "", "adbkey path (default $HOME/.config/scrcpy-webrtc/adbkey, same key as the Go server)")
	serverFlag := flag.String("server", "", "scrcpy-server binary (required with -full)")
	serverVersionFlag := flag.String("server-version", "3.1", "version argument passed to scrcpy-server")
	full := flag.Bool("full", false, "also test scrcpy push + video meta (occupies adbd — stop other clients first)")
	timeout := flag.Duration("timeout", 10*time.Second, "per-step timeout")
	videoEncoderFlag := flag.String("video-encoder", "", "pin video encoder, e.g. c2.android.avc.encoder")
	videoCodecFlag := flag.String("video-codec", "h264", "video codec passed to scrcpy-server")
	videoBitRateFlag := flag.Int("video-bit-rate", 0, "optional video bit rate (0 = scrcpy default)")
	audioFlag := flag.Bool("audio", true, "enable audio in the scrcpy probe")
	fullTimeout := flag.Duration("full-timeout", 30*time.Second, "watchdog for push/start/meta (prevents silent hangs)")
	flag.Parse()

	addr := *addrFlag
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid -addr %q: %v (expected host:port)\n", addr, err)
		os.Exit(2)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil || port < 1 || port > 65535 {
		fmt.Fprintf(os.Stderr, "invalid port in -addr %q\n", addr)
		os.Exit(2)
	}

	keyPath := *keyFlag
	if keyPath == "" {
		home, _ := os.UserHomeDir()
		keyPath = filepath.Join(home, ".config", "scrcpy-webrtc", "adbkey")
	}
	cred := adb.NewCredentialStore(keyPath, "scrcpy-webrtc")

	fmt.Println("==========================================================")
	fmt.Println(" scrcpy-webrtc Go  ADB 链路诊断")
	fmt.Println("==========================================================")
	fmt.Printf("目标   : %s\n", addr)
	fmt.Printf("adbkey : %s\n", keyPath)
	fmt.Printf("时间   : %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Println("提示   : 请先停掉 native 版和 fpk（redroid adbd 单客户端，同时只能一个连接）")
	fmt.Println()

	// ---- Step 1: raw TCP probe (single attempt, no retry) ----
	fmt.Println("[1/4] TCP 端口连通性（单次探测，端口是否开放）")
	t0 := time.Now()
	c, err := net.DialTimeout("tcp", addr, *timeout)
	tcpOK := err == nil
	if err != nil {
		fmt.Printf("      %s 连接失败: %v (%s)\n", mark(false), err, time.Since(t0))
		diagTCP(err)
	} else {
		fmt.Printf("      %s 连接成功: %s -> %s (%s)\n", mark(true), c.LocalAddr(), c.RemoteAddr(), time.Since(t0))
		_ = c.Close()
	}
	fmt.Println()

	// ---- Step 2: full ADB handshake, identical to the Go server ----
	fmt.Println("[2/4] ADB CNXN/AUTH 握手（与 Go 服务端一致：5 次退避重试 + 10s 超时）")
	t0 = time.Now()
	t, err := adb.Dial(host, port, cred, *timeout)
	elapsed := time.Since(t0)
	if err != nil {
		fmt.Printf("      %s 握手失败: %v (%s)\n", mark(false), err, elapsed)
		diagHandshake(err, addr)
		fmt.Println()
		conclusion(tcpOK, false, false, false)
		os.Exit(1)
	}
	defer t.Close()
	fmt.Printf("      %s 握手成功 (%s)\n", mark(true), elapsed)
	fmt.Println()

	// ---- Step 3: ADB shell getprop — this is exactly what turns a device green ----
	fmt.Println("[3/4] ADB shell getprop（等价于设备列表里的绿色在线状态）")
	props := []string{
		"ro.product.model",
		"ro.product.product",
		"ro.build.version.release",
		"ro.adb.secure",
		"ro.serialno",
	}
	shellAllOK := true
	for _, p := range props {
		v, e := t.GetProp(p)
		if e != nil {
			shellAllOK = false
			fmt.Printf("      %s getprop %-28s : %v\n", mark(false), p, e)
		} else {
			fmt.Printf("      %s getprop %-28s = %q\n", mark(true), p, v)
		}
	}
	fmt.Println()

	// ---- Step 4: optional scrcpy push + video meta ----
	scrcpyOK := false
	if *full {
		fmt.Println("[4/4] scrcpy 推流（push server.bin + reverse + 读 video meta）")
		fmt.Printf("      参数: server-version=%s codec=%s encoder=%q audio=%v bitrate=%d watchdog=%s\n",
			*serverVersionFlag, *videoCodecFlag, *videoEncoderFlag, *audioFlag, *videoBitRateFlag, *fullTimeout)
		bin, e := os.ReadFile(*serverFlag)
		if e != nil || len(bin) == 0 {
			fmt.Printf("      %s 无法加载 server.bin: %v（用 -server 指定 scrcpy-server）\n", mark(false), e)
		} else {
			opts := scrcpy.Options{
				ServerVersion: *serverVersionFlag,
				VideoCodec:    *videoCodecFlag,
				VideoEncoder:  *videoEncoderFlag,
				Audio:         boolPtr(*audioFlag),
			}
			if *videoBitRateFlag > 0 {
				opts.VideoBitRate = intPtr(*videoBitRateFlag)
			}
			fmt.Printf("      -> 正在执行 scrcpy.Start（push %.1f KB + reverse + accept streams）...\n",
				float64(len(bin))/1024)

			type startResult struct {
				streams *scrcpy.Streams
				err     error
			}
			startCh := make(chan startResult, 1)
			go func() {
				s, err := scrcpy.Start(t, bin, opts)
				startCh <- startResult{s, err}
			}()

			var streams *scrcpy.Streams
			var se error
			select {
			case r := <-startCh:
				streams, se = r.streams, r.err
			case <-time.After(*fullTimeout):
				fmt.Printf("      %s scrcpy.Start 超过 %s 未返回（可能卡在 push/reverse/accept）\n", mark(false), *fullTimeout)
				_ = t.Close()
				conclusion(tcpOK, true, shellAllOK, false)
				os.Exit(1)
			}
			if se != nil {
				fmt.Printf("      %s scrcpy 启动失败: %v\n", mark(false), se)
				fmt.Println("      ^ 失败点在 push / reverse / 启动 server / 接受流，见错误前缀")
			} else {
				defer streams.Close()
				fmt.Println("      -> scrcpy.Start 成功，开始打印 server stdout 并读 video meta ...")
				if streams.ServerOut != nil {
					go func() {
						scanner := bufio.NewScanner(streams.ServerOut)
						for scanner.Scan() {
							fmt.Printf("      [server] %s\n", scanner.Text())
						}
					}()
				}

				type metaResult struct {
					meta scrcpy.VideoMeta
					err  error
				}
				metaCh := make(chan metaResult, 1)
				go func() {
					m, err := streams.ReadVideoMeta(streams.Video)
					metaCh <- metaResult{m, err}
				}()
				select {
				case r := <-metaCh:
					if r.err != nil {
						fmt.Printf("      %s 读 video meta 失败: %v\n", mark(false), r.err)
					} else {
						scrcpyOK = true
						fmt.Printf("      %s video meta: codec=%d  %dx%d\n", mark(true), r.meta.Codec, r.meta.Width, r.meta.Height)
						fmt.Println("      ADB + scrcpy 链路完全正常；若前端仍不显示画面，问题在 WebRTC/前端而非 ADB。")
					}
				case <-time.After(*fullTimeout):
					fmt.Printf("      %s 读 video meta 超过 %s 未返回（已强制关闭流避免卡死）\n", mark(false), *fullTimeout)
					streams.Close()
					select {
					case <-metaCh:
					case <-time.After(time.Second):
					}
				}
			}
		}
	} else {
		fmt.Println("[4/4] scrcpy 推流：跳过（加 -full -server <server.bin> 启用；会占用 adbd）")
	}
	fmt.Println()

	conclusion(tcpOK, true, shellAllOK, scrcpyOK)
}

// diagTCP explains the common TCP-level failure modes.
func diagTCP(err error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no route to host"), strings.Contains(msg, "network is unreachable"):
		fmt.Println("      -> ICMP 负缓存 / 路由问题：telnet 可能通但内核缓存了不可达。")
		fmt.Println("         确认 NAS 到该网段的路由；Go 服务端已有 5 次退避重试可跨过瞬时缓存。")
	case strings.Contains(msg, "refused"):
		fmt.Println("      -> 端口未监听：检查 172.16.2.95 上的代理/映射服务是否启动、端口是否正确。")
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "timed out"):
		fmt.Println("      -> 被防火墙丢包（不是拒绝）：检查安全组 / iptables / 容器端口映射。")
	default:
		fmt.Println("      -> 网络层失败，先解决 TCP 可达性再看 ADB。")
	}
}

// diagHandshake explains failures after TCP succeeded — the most common case
// for "一直连接中" with a green saved connection.
func diagHandshake(err error, addr string) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "stayed silent"), strings.Contains(msg, "handshake timeout"):
		fmt.Println("      -> 端口能连上，但对端没有按 ADB 协议应答（CNXN/AUTH 静默）。典型原因：")
		fmt.Println("         1) adbd 被另一个客户端占用（native 版 / 另一个 fpk 实例）——这是最常见原因；")
		fmt.Println("         2) 该地址不是纯 TCP 的 adb 转发，而是 HTTP CONNECT / SOCKS 代理——")
		fmt.Println("            Go 服务端是直连 ADB，不做代理协商，会一直等到握手超时；")
		fmt.Println("         3) 端口映射到了非 adb 服务。")
		fmt.Printf("         验证：先在 NAS 执行 adb connect %s，若也卡住即为 adbd 被占用/非 adb 端口。\n", addr)
		fmt.Println("         若该 key 未授权，可加 -key 指向 fpk 正在用的 adbkey 再试。")
	case strings.Contains(msg, "refused"), strings.Contains(msg, "no route to host"):
		fmt.Println("      -> 握手阶段网络中断，回到第 1 步排查 TCP/路由。")
	default:
		fmt.Println("      -> ADB 协议层失败；若反复 AUTH 超时，多为 adbkey 未被信任或 adbd 被占用。")
		fmt.Println("         若错误文本里出现 HTTP/407/Proxy 等字样，说明该端口是 HTTP 代理而非纯 TCP adb 转发。")
	}
}

func conclusion(tcpOK, handshakeOK, shellOK, scrcpyOK bool) {
	fmt.Println("==========================================================")
	fmt.Println(" 结论")
	fmt.Println("==========================================================")
	switch {
	case !handshakeOK:
		if !tcpOK {
			fmt.Println(" TCP 不通且 ADB 握手失败 —— Go 版的“绿色”是旧快照，真实拨号失败在网络层。")
		} else {
			fmt.Println(" TCP 通但 ADB 握手失败 —— “一直连接中/连不上”就卡在这里。")
			fmt.Println(" 优先处理：停掉 native 版/fpk，确认 172.16.2.95:5555 是纯 TCP adb 转发而非 HTTP/SOCKS 代理。")
		}
	case !tcpOK:
		fmt.Println(" 单次 TCP 探测超时，但带 5 次重试的拨号成功 —— TCP 链路不稳定（SYN 偶发被丢）。")
		fmt.Println(" Go 服务端的重试能覆盖大部分情况；若仍频繁失败需排查防火墙/网络抖动。")
	case !shellOK:
		fmt.Println(" ADB 已握手但 shell 失败 —— getprop 异常，检查 redroid 系统状态。")
	case !scrcpyOK:
		fmt.Println(" ADB shell 正常（设备会显示在线），但 scrcpy 推流未验证或未成功。")
		fmt.Println(" 若页面“不显示画面”，用 -full 复测以定位 push/reverse/视频流。")
	default:
		fmt.Println(" 全链路通过：TCP -> ADB 握手 -> getprop -> scrcpy 视频流均正常。")
	}
}
