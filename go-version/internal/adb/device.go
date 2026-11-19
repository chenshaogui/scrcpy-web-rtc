package adb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// GetProp runs `getprop <key>` on the device.
func (t *Transport) GetProp(key string) (string, error) {
	out, err := t.RunCommand("exec:getprop "+key, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// DeviceEntry is a saved wireless-adb target.
type DeviceEntry struct {
	Serial  string `json:"serial"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Model   string `json:"model"`
	Product string `json:"product"`
	AddedAt string `json:"addedAt,omitempty"`
}

// DeviceInfo is the API shape returned to the browser.
type DeviceInfo struct {
	Serial  string `json:"serial"`
	Product string `json:"product"`
	Model   string `json:"model"`
	Offline bool   `json:"offline,omitempty"`
}

// DeviceStore persists adb connect targets in a JSON file and probes each via
// a direct AdbDaemonTransport. Drop-in replacement for @yume-chan's DeviceStore.
type DeviceStore struct {
	path   string
	cred   *CredentialStore
	mu     sync.Mutex
	cache  []DeviceEntry
	loaded bool
}

func NewDeviceStore(devicesFile, varDir string) *DeviceStore {
	var path string
	if devicesFile != "" {
		path = devicesFile
	} else if varDir != "" {
		path = filepath.Join(varDir, "devices.json")
	}
	keyPath := ""
	if varDir != "" {
		keyPath = filepath.Join(varDir, "adbkey")
	}
	return &DeviceStore{path: path, cred: NewCredentialStore(keyPath, "scrcpy-webrtc")}
}

func (d *DeviceStore) read() []DeviceEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.loaded {
		return d.cache
	}
	d.loaded = true
	if d.path != "" {
		if data, err := os.ReadFile(d.path); err == nil {
			var list []DeviceEntry
			if json.Unmarshal(data, &list) == nil {
				d.cache = list
			}
		}
	}
	return d.cache
}

func (d *DeviceStore) write(list []DeviceEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cache = list
	d.loaded = true
	if d.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(d.path), 0o755); err != nil {
		return
	}
	data, _ := json.MarshalIndent(list, "", "  ")
	_ = os.WriteFile(d.path, data, 0o644)
}

func (d *DeviceStore) probe(host string, port int) (model, product string, err error) {
	t, err := Dial(host, port, d.cred, 10*time.Second)
	if err != nil {
		return "", "", err
	}
	defer t.Close()
	model, err = t.GetProp("ro.product.model")
	if err != nil {
		model = ""
	}
	product, err = t.GetProp("ro.product.product")
	if err != nil {
		product = ""
	}
	return model, product, nil
}

func shortModel(product, model string) string {
	s := model
	if s == "" {
		s = product
	}
	return strings.ReplaceAll(strings.TrimSpace(s), "_", " ")
}

// List probes every saved target and returns the API shape.
func (d *DeviceStore) List() []DeviceInfo {
	list := d.read()
	out := make([]DeviceInfo, 0, len(list))
	for _, dev := range list {
		model, product, err := d.probe(dev.Host, dev.Port)
		info := DeviceInfo{Serial: dev.Serial, Product: dev.Product, Model: dev.Model}
		if err != nil {
			info.Offline = true
		} else {
			if product != "" {
				info.Product = product
			}
			if m := shortModel(product, model); m != "" {
				info.Model = m
			}
		}
		out = append(out, info)
	}
	return out
}

// Connect probes addr ("host:port"), then saves it.
func (d *DeviceStore) Connect(addr string) (DeviceEntry, error) {
	parts := strings.Split(addr, ":")
	if len(parts) != 2 {
		return DeviceEntry{}, fmt.Errorf("invalid adb address: %s", addr)
	}
	host := parts[0]
	var port int
	if _, err := fmt.Sscanf(parts[1], "%d", &port); err != nil || port < 1 || port > 65535 {
		return DeviceEntry{}, fmt.Errorf("invalid adb address: %s", addr)
	}
	model, product, err := d.probe(host, port)
	if err != nil {
		return DeviceEntry{}, err
	}
	serial := fmt.Sprintf("%s:%d", host, port)
	entry := DeviceEntry{
		Serial: serial, Host: host, Port: port,
		Model: shortModel(product, model), Product: product,
		AddedAt: time.Now().UTC().Format(time.RFC3339),
	}
	list := d.read()
	replaced := false
	for i := range list {
		if list[i].Serial == serial {
			list[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, entry)
	}
	d.write(list)
	return entry, nil
}

// Disconnect removes a saved target. Returns true if something was removed.
func (d *DeviceStore) Disconnect(serial string) bool {
	list := d.read()
	next := list[:0]
	removed := false
	for _, dev := range list {
		if dev.Serial == serial {
			removed = true
			continue
		}
		next = append(next, dev)
	}
	if removed {
		d.write(next)
	}
	return removed
}

// DialSerial opens a direct transport to a saved device.
func (d *DeviceStore) DialSerial(serial string) (*Transport, error) {
	return d.DialSerialContext(context.Background(), serial)
}

// DialSerialContext is DialSerial with cancellation (see DialContext).
func (d *DeviceStore) DialSerialContext(ctx context.Context, serial string) (*Transport, error) {
	for _, dev := range d.read() {
		if dev.Serial == serial {
			return DialContext(ctx, dev.Host, dev.Port, d.cred, 10*time.Second)
		}
	}
	return nil, fmt.Errorf("device not found: %s — open Settings and add the device with the ADB connect form (device IP + port) first", serial)
}

// ShellProtocol implements adb shell v2 packet framing.
type ShellPacket struct {
	ID   byte
	Data []byte
}

const (
	ShellStdin      byte = 0
	ShellStdout     byte = 1
	ShellStderr     byte = 2
	ShellExit       byte = 3
	ShellCloseStdin byte = 4
	ShellResize     byte = 5
)

// OpenShell opens a shell v2 PTY socket.
func (t *Transport) OpenShell(term string) (*Socket, error) {
	if term == "" {
		term = "xterm-256color"
	}
	return t.OpenSocket("shell,v2,pty,TERM=" + term + ":")
}

// WriteShellPacket writes one shell v2 packet (id + u32 LE length + data).
func WriteShellPacket(s *Socket, id byte, data []byte) error {
	var hdr [5]byte
	hdr[0] = id
	le32(hdr[1:5], uint32(len(data)))
	if _, err := s.Write(hdr[:]); err != nil {
		return err
	}
	if len(data) > 0 {
		_, err := s.Write(data)
		return err
	}
	return nil
}

func le32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}
