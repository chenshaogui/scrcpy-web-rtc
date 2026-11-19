// Package signal implements serverless signaling: small AES-256-GCM encrypted
// JSON messages relayed through public MQTT brokers over WebSocket. It is a Go
// port of signal.mjs, using only the MQTT 3.1.1 QoS-0 subset we need.
package signal

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var DefaultBrokers = []string{
	"wss://broker.emqx.io:8084/mqtt",
	"wss://broker-cn.emqx.io:8084/mqtt",
	"wss://broker.hivemq.com:8884/mqtt",
}

// mqttStr encodes a UTF-8 string as 2-byte length prefix + bytes.
func mqttStr(s string) []byte {
	b := []byte(s)
	out := make([]byte, 2+len(b))
	binary.BigEndian.PutUint16(out[:2], uint16(len(b)))
	copy(out[2:], b)
	return out
}

// mqttPacket prefixes a packet type byte and a variable-length remaining length.
func mqttPacket(pktType byte, body []byte) []byte {
	var length []byte
	n := len(body)
	for {
		d := byte(n % 128)
		n /= 128
		if n > 0 {
			d |= 0x80
		}
		length = append(length, d)
		if n == 0 {
			break
		}
	}
	out := []byte{pktType}
	out = append(out, length...)
	out = append(out, body...)
	return out
}

// mqttClient is one broker connection.
type mqttClient struct {
	url       string
	ws        *websocket.Conn
	onPublish func(topic string, payload []byte)
	onReady   func()
	log       func(string, ...interface{})

	mu       sync.Mutex
	ready    bool
	closed   bool
	topics   map[string]bool
	queue    [][2]interface{} // [topic, payload]
	buf      []byte
	pingStop chan struct{}
	retry    *time.Timer
}

func newMQTTClient(url string, onPublish func(string, []byte), onReady func(), log func(string, ...interface{})) *mqttClient {
	c := &mqttClient{
		url: url, onPublish: onPublish, onReady: onReady, log: log,
		topics: make(map[string]bool), pingStop: make(chan struct{}),
	}
	go c.connect()
	return c
}

func (c *mqttClient) connect() {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, Subprotocols: []string{"mqtt"}}
	ws, resp, err := dialer.Dial(c.url, http.Header{})
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		c.reconnect()
		return
	}
	c.mu.Lock()
	c.ws = ws
	c.buf = nil
	c.mu.Unlock()

	ws.SetPingHandler(func(string) error { return nil })
	// Send CONNECT on open.
	idBytes := make([]byte, 8)
	_, _ = rand.Read(idBytes)
	id := "scrcpy-webrtc-" + hex.EncodeToString(idBytes)
	connectBody := append(mqttStr("MQTT"), 4, 0x02, 0, 60)
	connectBody = append(connectBody, mqttStr(id)...)
	_ = ws.WriteMessage(websocket.BinaryMessage, mqttPacket(0x10, connectBody))

	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			c.reconnect()
			return
		}
		c.feed(data)
	}
}

func (c *mqttClient) reconnect() {
	c.mu.Lock()
	c.ready = false
	select {
	case <-c.pingStop:
	default:
		close(c.pingStop)
	}
	c.pingStop = make(chan struct{})
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	if c.retry != nil {
		c.retry.Stop()
	}
	c.retry = time.AfterFunc(5*time.Second, func() { go c.connect() })
}

// feed handles the possibility that a WS frame has multiple or partial MQTT packets.
func (c *mqttClient) feed(chunk []byte) {
	c.mu.Lock()
	c.buf = append(c.buf, chunk...)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		if len(c.buf) < 2 {
			c.mu.Unlock()
			return
		}
		// Parse remaining length.
		var length, mult int = 0, 1
		i := 1
		malformed := false
		for {
			if i >= len(c.buf) {
				c.mu.Unlock()
				return
			}
			d := c.buf[i]
			i++
			length += int(d&0x7f) * mult
			if d&0x80 == 0 {
				break
			}
			mult *= 128
			if i > 5 {
				c.buf = nil
				malformed = true
				break
			}
		}
		if malformed {
			c.mu.Unlock()
			continue
		}
		total := i + length
		if len(c.buf) < total {
			c.mu.Unlock()
			return
		}
		first := c.buf[0]
		body := append([]byte(nil), c.buf[i:total]...)
		c.buf = c.buf[total:]
		c.mu.Unlock()
		c.packet(first, body)
	}
}

func (c *mqttClient) packet(first byte, body []byte) {
	typ := first & 0xf0
	switch typ {
	case 0x20: // CONNACK
		if len(body) < 2 || body[1] != 0 {
			c.log("mqtt: %s refused connection", c.url)
			c.mu.Lock()
			c.closed = true
			c.mu.Unlock()
			_ = c.ws.Close()
			return
		}
		c.mu.Lock()
		c.ready = true
		topics := make([]string, 0, len(c.topics))
		for t := range c.topics {
			topics = append(topics, t)
		}
		queued := c.queue
		c.queue = nil
		c.mu.Unlock()
		for _, t := range topics {
			c.sub(t)
		}
		for _, item := range queued {
			c.publish(item[0].(string), item[1].([]byte))
		}
		// Start PING timer.
		go func() {
			stop := c.pingStop
			ticker := time.NewTicker(25 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					c.sendRaw([]byte{0xc0, 0})
				case <-stop:
					return
				}
			}
		}()
		if c.onReady != nil {
			c.onReady()
		}
	case 0x30: // PUBLISH
		if len(body) < 2 {
			return
		}
		tlen := int(binary.BigEndian.Uint16(body[:2]))
		off := 2 + tlen
		if qos := (first >> 1) & 3; qos > 0 {
			off += 2
		}
		if off > len(body) || 2+tlen > len(body) {
			return
		}
		topic := string(body[2 : 2+tlen])
		payload := body[off:]
		if c.onPublish != nil {
			c.onPublish(topic, payload)
		}
	}
}

func (c *mqttClient) sendRaw(pkt []byte) {
	c.mu.Lock()
	ws := c.ws
	c.mu.Unlock()
	if ws == nil {
		return
	}
	_ = ws.WriteMessage(websocket.BinaryMessage, pkt)
}

func (c *mqttClient) sub(topic string) {
	c.sendRaw(mqttPacket(0x82, append([]byte{0, 1}, mqttStr(topic)...)))
}

func (c *mqttClient) subscribe(topic string) {
	c.mu.Lock()
	c.topics[topic] = true
	ready := c.ready
	c.mu.Unlock()
	if ready {
		c.sub(topic)
	}
}

func (c *mqttClient) publish(topic string, payload []byte) {
	c.mu.Lock()
	ready := c.ready
	c.mu.Unlock()
	if ready {
		body := append(mqttStr(topic), payload...)
		c.sendRaw(mqttPacket(0x30, body))
	} else {
		c.mu.Lock()
		if len(c.queue) < 64 {
			c.queue = append(c.queue, [2]interface{}{topic, payload})
		}
		c.mu.Unlock()
	}
}

func (c *mqttClient) close() {
	c.mu.Lock()
	c.closed = true
	ws := c.ws
	c.mu.Unlock()
	if ws != nil {
		_ = ws.Close()
	}
	if c.retry != nil {
		c.retry.Stop()
	}
}

var errNoBroker = errors.New("no signaling broker reachable")

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
