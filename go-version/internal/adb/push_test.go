package adb

import (
	"bytes"
	"fmt"
	"net"
	"testing"
	"time"
)

// mockSyncAdbd simulates the ADB sync protocol for pushing a file. It replies
// to OPEN(sync:) with an empty OKAY (no delayed ack), reads SEND/DATA/DONE
// packets and replies with a single OKAY, mirroring a real adbd in non-delayed
// ack mode (which is what our CNXN banner negotiates).
func mockSyncAdbd(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
				// Handshake: CNXN then AUTH (accept any signature).
				pkt, err := readPacket(conn)
				if err != nil || pkt.Command != CmdConnect {
					return
				}
				_ = writePacket(conn, Packet{Command: CmdConnect, Arg0: initialVersion, Arg1: initialMaxPayload}, initialVersion, true)
				// Device accepts without AUTH. Now handle socket requests.
				for {
					pkt, err := readPacket(conn)
					if err != nil {
						return
					}
					switch pkt.Command {
					case CmdOpen:
						// reply OKAY with empty payload (arg0=our id, arg1=their local id)
						_ = writePacket(conn, Packet{Command: CmdOkay, Arg0: pkt.Arg0 + 1000, Arg1: pkt.Arg0}, initialVersion, false)
					case CmdWrite:
						// ack every WRITE with an empty OKAY (non-delayed-ack)
						_ = writePacket(conn, Packet{Command: CmdOkay, Arg0: pkt.Arg1, Arg1: pkt.Arg0}, initialVersion, false)
						// DONE sync request is exactly 8 bytes: "DONE" + u32 mtime.
						if len(pkt.Payload) == 8 && string(pkt.Payload[0:4]) == "DONE" {
							// reply sync OKAY (id + u32 unused) as a WRTE back
							syncOk := append([]byte("OKAY"), make([]byte, 4)...)
							_ = writePacket(conn, Packet{Command: CmdWrite, Arg0: pkt.Arg1 + 1000, Arg1: pkt.Arg0, Payload: syncOk}, initialVersion, false)
						}
					case CmdClose:
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func TestPushFileNoDeadlock(t *testing.T) {
	addr, stop := mockSyncAdbd(t)
	defer stop()
	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	cred := NewCredentialStore("", "push-test")
	tr, err := Dial(host, port, cred, 3*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer tr.Close()

	data := bytes.Repeat([]byte{0xAB}, 256*1024) // 256KB, multiple 1MB? fits one; use 2MB
	data = bytes.Repeat([]byte{0xAB}, 2*1024*1024)
	done := make(chan error, 1)
	go func() {
		err := tr.PushFile(data, "/data/local/tmp/scrcpy-server.jar", time.Now().Unix())
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("PushFile: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("PushFile deadlocked (would hang scrcpy startup -> stuck at 'connecting')")
	}
}
