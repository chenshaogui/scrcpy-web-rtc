package signal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

func b64uEncode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func b64uDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// Room is an encrypted signaling room. Messages are AES-256-GCM encrypted and
// the topic is a one-way hash of the key, so brokers see only ciphertext.
type Room struct {
	key      []byte
	gcm      cipher.AEAD
	pubTopic string
	clients  []*mqttClient

	mu        sync.Mutex
	chain     chan struct{} // serialize onmessage delivery
	seen      map[string]bool
	seenOrder []string

	OnMessage func(map[string]interface{})
	log       func(string, ...interface{})
}

// NewRoom creates a room, connects to all brokers and resolves once any one
// of them accepts the connection (or errNoBroker after waitMs).
func NewRoom(key []byte, role string, brokers []string, log func(string, ...interface{}), waitMs int) (*Room, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte("scrcpy-webrtc-room:" + b64uEncode(key)))
	rid := hex.EncodeToString(digest[:10])
	me := "h"
	if role != "host" {
		me = "v"
	}
	pub := "scrcpy-webrtc/" + rid + "/" + me
	sub := "scrcpy-webrtc/" + rid + "/"
	if me == "h" {
		sub += "v"
	} else {
		sub += "h"
	}

	if len(brokers) == 0 {
		brokers = DefaultBrokers
	}
	r := &Room{
		key: key, gcm: gcm, pubTopic: pub,
		chain: make(chan struct{}, 1),
		seen:  make(map[string]bool),
		log:   log,
	}
	r.chain <- struct{}{}

	ready := make(chan struct{}, len(brokers))
	var once sync.Once
	onReady := func() { once.Do(func() { ready <- struct{}{} }) }
	for _, url := range brokers {
		client := newMQTTClient(url, func(_ string, payload []byte) { r.recv(payload) }, onReady, log)
		client.subscribe(sub)
		r.clients = append(r.clients, client)
	}

	select {
	case <-ready:
		return r, nil
	case <-time.After(time.Duration(waitMs) * time.Millisecond):
		r.Close()
		return nil, errNoBroker
	}
}

func (r *Room) recv(payload []byte) {
	// Serialize decryption/dispatch so message order survives across brokers.
	<-r.chain
	defer func() { r.chain <- struct{}{} }()

	if len(payload) < 29 {
		return // 12 iv + 16 tag + at least 1 content byte
	}
	id := hex.EncodeToString(payload[:12])
	r.mu.Lock()
	if r.seen[id] {
		r.mu.Unlock()
		return
	}
	r.seen[id] = true
	r.seenOrder = append(r.seenOrder, id)
	if len(r.seenOrder) > 1024 {
		oldest := r.seenOrder[0]
		delete(r.seen, oldest)
		r.seenOrder = r.seenOrder[1:]
	}
	r.mu.Unlock()

	nonce := payload[:12]
	plaintext, err := r.gcm.Open(nil, nonce, payload[12:], nil)
	if err != nil {
		return // not ours or corrupted
	}
	var msg map[string]interface{}
	if json.Unmarshal(plaintext, &msg) != nil {
		return
	}
	if r.OnMessage != nil {
		r.OnMessage(msg)
	}
}

// Send encrypts and publishes an object on every connected broker.
func (r *Room) Send(obj interface{}) error {
	plaintext, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	payload := r.gcm.Seal(nonce, nonce, plaintext, nil)
	for _, c := range r.clients {
		c.publish(r.pubTopic, payload)
	}
	return nil
}

// Close closes all broker connections.
func (r *Room) Close() {
	for _, c := range r.clients {
		c.close()
	}
}
