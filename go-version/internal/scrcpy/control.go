package scrcpy

import (
	"bytes"
	"encoding/binary"
)

// Control message type bytes (scrcpy 3.1).
const (
	CtrlInjectKeyCode byte = 0
	CtrlInjectText    byte = 1
	CtrlInjectTouch   byte = 2
	CtrlInjectScroll  byte = 3
	CtrlSetClipboard  byte = 9
	CtrlResetVideo    byte = 17
)

// Android motion event actions.
const (
	ActionDown   byte = 0
	ActionUp     byte = 1
	ActionMove   byte = 2
	ActionCancel byte = 3
)

// ScrcpyPointerId.Finger = -2 as a 64-bit two's complement value.
const PointerFinger uint64 = 0xfffffffffffffffe

func putU32BE(b *bytes.Buffer, v uint32) {
	var t [4]byte
	binary.BigEndian.PutUint32(t[:], v)
	b.Write(t[:])
}

func putU16BE(b *bytes.Buffer, v uint16) {
	var t [2]byte
	binary.BigEndian.PutUint16(t[:], v)
	b.Write(t[:])
}

func putU64BE(b *bytes.Buffer, v uint64) {
	var t [8]byte
	binary.BigEndian.PutUint64(t[:], v)
	b.Write(t[:])
}

// UnsignedFloat: clamp [-1,1], 1 -> 0xffff, else v*0x10000, stored u16 BE.
func unsignedFloat(v float32) uint16 {
	if v > 1 {
		v = 1
	} else if v < -1 {
		v = -1
	}
	if v == 1 {
		return 0xffff
	}
	return uint16(v * 0x10000)
}

// signedFloat: clamp [-1,1], 1 -> 0x7fff, else v*0x8000, stored i16 BE.
func signedFloat(v float32) uint16 {
	if v > 1 {
		v = 1
	} else if v < -1 {
		v = -1
	}
	if v == 1 {
		return 0x7fff
	}
	return uint16(int16(v * 0x8000))
}

// InjectTouch encodes a 32-byte touch control message.
func InjectTouch(action byte, pointerX, pointerY uint32, videoW, videoH uint16, pressure, buttons uint32) []byte {
	var b bytes.Buffer
	b.WriteByte(CtrlInjectTouch)
	b.WriteByte(action)
	putU64BE(&b, PointerFinger)
	putU32BE(&b, pointerX)
	putU32BE(&b, pointerY)
	putU16BE(&b, videoW)
	putU16BE(&b, videoH)
	putU16BE(&b, unsignedFloat(float32(pressure)))
	putU32BE(&b, 0) // actionButton
	putU32BE(&b, buttons)
	return b.Bytes()
}

// InjectScroll encodes a 21-byte scroll control message.
func InjectScroll(pointerX, pointerY uint32, videoW, videoH uint16, scrollX, scrollY float32, buttons uint32) []byte {
	var b bytes.Buffer
	b.WriteByte(CtrlInjectScroll)
	putU32BE(&b, pointerX)
	putU32BE(&b, pointerY)
	putU16BE(&b, videoW)
	putU16BE(&b, videoH)
	putU16BE(&b, signedFloat(scrollX))
	putU16BE(&b, signedFloat(scrollY))
	putU32BE(&b, buttons)
	return b.Bytes()
}

// InjectKeyCode encodes a 14-byte key control message.
func InjectKeyCode(action, keyCode, repeat, metaState uint32) []byte {
	var b bytes.Buffer
	b.WriteByte(CtrlInjectKeyCode)
	b.WriteByte(byte(action))
	putU32BE(&b, keyCode)
	putU32BE(&b, repeat)
	putU32BE(&b, metaState)
	return b.Bytes()
}

// InjectText encodes a text control message: type + u32 BE byte length + UTF-8.
func InjectText(text string) []byte {
	var b bytes.Buffer
	b.WriteByte(CtrlInjectText)
	putU32BE(&b, uint32(len(text)))
	b.WriteString(text)
	return b.Bytes()
}

// SetClipboard encodes a clipboard control message with a u64 sequence.
func SetClipboard(sequence uint64, paste bool, content string) []byte {
	var b bytes.Buffer
	b.WriteByte(CtrlSetClipboard)
	putU64BE(&b, sequence)
	if paste {
		b.WriteByte(1)
	} else {
		b.WriteByte(0)
	}
	putU32BE(&b, uint32(len(content)))
	b.WriteString(content)
	return b.Bytes()
}

// ResetVideo asks the device for a fresh keyframe.
func ResetVideo() []byte {
	return []byte{CtrlResetVideo}
}
