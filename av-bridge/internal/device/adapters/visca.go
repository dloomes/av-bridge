package adapters

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// VISCA-over-IP protocol primitives.
//
// Wire format (Sony VISCA-over-IP spec §15.9): every UDP datagram is an
// 8-byte header followed by 1-16 bytes of VISCA payload.
//
//   Byte 0-1: payload type (see viscaPayload* constants)
//   Byte 2-3: payload length in bytes, big-endian
//   Byte 4-7: sequence number, big-endian, monotonic per session
//   Byte 8+ : VISCA payload — the exact byte sequence the serial protocol
//             uses, e.g. 81 01 04 00 02 FF for CAM_Power ON.
//
// Over IP, the VISCA address bytes are pinned: controller is always 0 and
// the camera is always 1, so command headers are always 0x81 and reply
// headers are always 0x90. (Serial VISCA supports 1-7 daisy-chained
// cameras; IP does not — one camera per socket, addressed only by IP.)
//
// This file is transport-agnostic — it just builds and parses byte
// sequences. The UDP send/receive loop lives in visca_over_ip.go.

// Payload type IDs from the Sony spec §15.10. Two-byte big-endian value.
const (
	viscaPayloadCommand       uint16 = 0x0100 // VISCA command from controller
	viscaPayloadInquiry       uint16 = 0x0110 // VISCA inquiry from controller
	viscaPayloadReply         uint16 = 0x0111 // VISCA reply from camera
	viscaPayloadDeviceSetting uint16 = 0x0120 // e.g. Address / IF_Clear
	viscaPayloadControlCmd    uint16 = 0x0200 // e.g. RESET sequence-number
	viscaPayloadControlReply  uint16 = 0x0201
)

// VISCA reply category — extracted from the second byte of the payload.
// The low nibble carries the socket number (Y) or, for inquiry replies,
// zero. Callers care about the category, not the socket, so we mask.
const (
	viscaReplyACK        = 0x40 // command accepted — completion still pending
	viscaReplyCompletion = 0x50 // command finished / inquiry data present
	viscaReplyError      = 0x60 // command rejected — 2nd payload byte is error code
)

// VISCA error codes from §11 error table.
var viscaErrorText = map[byte]string{
	0x01: "message length error",
	0x02: "syntax error",
	0x03: "command buffer full",
	0x04: "command cancelled",
	0x05: "no socket",
	0x41: "command not executable",
}

// The over-IP VISCA header prefix on every command. Controller address 0
// with the receiver-marker bit set for camera 1 gives 0x81.
const viscaCmdHeader byte = 0x81

// The over-IP VISCA reply header — camera 1 answering controller 0.
const viscaReplyHeader byte = 0x90

// The VISCA packet terminator.
const viscaTerminator byte = 0xFF

// encodeViscaIP wraps a raw VISCA payload (starting with 0x81, ending with
// 0xFF) in the 8-byte over-IP transport header.
func encodeViscaIP(payloadType uint16, seq uint32, payload []byte) []byte {
	if len(payload) < 1 || len(payload) > 16 {
		// Callers are internal — this would only fire on a programming
		// mistake, so a panic is more honest than an error return that
		// nothing checks. VISCA payload is always 3–16 bytes in practice.
		panic(fmt.Sprintf("visca: payload length %d out of range 1..16", len(payload)))
	}
	buf := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint16(buf[0:2], payloadType)
	binary.BigEndian.PutUint16(buf[2:4], uint16(len(payload)))
	binary.BigEndian.PutUint32(buf[4:8], seq)
	copy(buf[8:], payload)
	return buf
}

// decodeViscaIP splits an inbound datagram into (payload_type, seq, payload).
// Returns an error if the datagram is malformed — the caller drops it.
func decodeViscaIP(datagram []byte) (payloadType uint16, seq uint32, payload []byte, err error) {
	if len(datagram) < 9 {
		return 0, 0, nil, fmt.Errorf("visca: datagram too short (%d bytes)", len(datagram))
	}
	payloadType = binary.BigEndian.Uint16(datagram[0:2])
	length := binary.BigEndian.Uint16(datagram[2:4])
	seq = binary.BigEndian.Uint32(datagram[4:8])
	if int(length) != len(datagram)-8 {
		return 0, 0, nil, fmt.Errorf("visca: header length %d != actual %d", length, len(datagram)-8)
	}
	payload = datagram[8:]
	if payload[len(payload)-1] != viscaTerminator {
		return 0, 0, nil, errors.New("visca: payload not terminated with FF")
	}
	return payloadType, seq, payload, nil
}

// viscaReplyKind pulls the category (ACK / Completion / Error) out of a
// reply payload. Returns 0 if the payload isn't a recognised reply shape.
func viscaReplyKind(payload []byte) byte {
	if len(payload) < 3 || payload[0] != viscaReplyHeader {
		return 0
	}
	// Second byte high nibble is category, low nibble is socket.
	return payload[1] & 0xF0
}

// viscaErrorMessage decodes an error payload (X0 6Y ee FF) into text.
func viscaErrorMessage(payload []byte) string {
	if len(payload) < 4 {
		return "visca error (malformed)"
	}
	code := payload[2]
	if s, ok := viscaErrorText[code]; ok {
		return fmt.Sprintf("visca error 0x%02X: %s", code, s)
	}
	return fmt.Sprintf("visca error 0x%02X (unknown)", code)
}

// -----------------------------------------------------------------------------
// Command builders — every function returns a raw VISCA payload
// (0x81 ... 0xFF), which encodeViscaIP then wraps for the wire.
// -----------------------------------------------------------------------------

// viscaIFClear resets the camera's command state (clears both sockets).
// Sent on connect so a stale in-flight command from a previous session
// can't confuse the ACK/Completion state machine.
func viscaIFClear() []byte {
	return []byte{viscaCmdHeader, 0x01, 0x00, 0x01, viscaTerminator}
}

// viscaPower builds CAM_Power ON (true) or OFF/Standby (false).
func viscaPower(on bool) []byte {
	v := byte(0x03) // Standby
	if on {
		v = 0x02
	}
	return []byte{viscaCmdHeader, 0x01, 0x04, 0x00, v, viscaTerminator}
}

// viscaZoomStop / viscaZoomTele / viscaZoomWide — variable-speed zoom in
// / out (speed 0-7, 0 slowest, 7 fastest). Speed clamped defensively.
func viscaZoomStop() []byte {
	return []byte{viscaCmdHeader, 0x01, 0x04, 0x07, 0x00, viscaTerminator}
}

func viscaZoomTele(speed byte) []byte {
	return []byte{viscaCmdHeader, 0x01, 0x04, 0x07, 0x20 | (speed & 0x07), viscaTerminator}
}

func viscaZoomWide(speed byte) []byte {
	return []byte{viscaCmdHeader, 0x01, 0x04, 0x07, 0x30 | (speed & 0x07), viscaTerminator}
}

// viscaZoomDirect drives the zoom to an absolute position (0x0000 wide,
// 0x4000 tele-max for optical). Value is split into four nibbles pqrs.
func viscaZoomDirect(pos uint16) []byte {
	if pos > 0x4000 {
		pos = 0x4000
	}
	p := byte((pos >> 12) & 0x0F)
	q := byte((pos >> 8) & 0x0F)
	r := byte((pos >> 4) & 0x0F)
	s := byte(pos & 0x0F)
	return []byte{viscaCmdHeader, 0x01, 0x04, 0x47, p, q, r, s, viscaTerminator}
}

// viscaFocusAuto / viscaFocusManual — switch between AF and MF.
func viscaFocusAuto() []byte {
	return []byte{viscaCmdHeader, 0x01, 0x04, 0x38, 0x02, viscaTerminator}
}

func viscaFocusManual() []byte {
	return []byte{viscaCmdHeader, 0x01, 0x04, 0x38, 0x03, viscaTerminator}
}

// viscaFocusOnePush triggers a one-shot AF cycle (only meaningful while
// the camera is in Manual Focus mode per the VC-A61P doc §11).
func viscaFocusOnePush() []byte {
	return []byte{viscaCmdHeader, 0x01, 0x04, 0x18, 0x01, viscaTerminator}
}

// viscaPresetRecall / viscaPresetSet / viscaPresetClear — preset number
// 0-255. VC-A61P uses two commands to cover 0-127 and 128-255; older Sony
// devices support 0-127 only. We use the extended form (3F 02) which the
// VC-A61P doc lists for the whole range.
func viscaPresetRecall(n byte) []byte {
	return []byte{viscaCmdHeader, 0x01, 0x04, 0x3F, 0x02, n, viscaTerminator}
}

func viscaPresetSet(n byte) []byte {
	return []byte{viscaCmdHeader, 0x01, 0x04, 0x3F, 0x01, n, viscaTerminator}
}

// viscaPanTilt drives the camera continuously — vv = pan speed (0-0x18),
// ww = tilt speed (0-0x14), then two direction bytes. Callers use
// viscaPanTiltStop to halt. Speeds are clamped to the Sony maxima; a
// caller passing 0xFF gets the fastest legal value, not undefined.
func viscaPanTilt(panSpeed, tiltSpeed byte, panDir, tiltDir byte) []byte {
	if panSpeed > 0x18 {
		panSpeed = 0x18
	}
	if tiltSpeed > 0x14 {
		tiltSpeed = 0x14
	}
	return []byte{viscaCmdHeader, 0x01, 0x06, 0x01, panSpeed, tiltSpeed, panDir, tiltDir, viscaTerminator}
}

// Direction bytes for Pan-Tilt Drive.
const (
	viscaPanLeft  byte = 0x01
	viscaPanRight byte = 0x02
	viscaPanStop  byte = 0x03
	viscaTiltUp   byte = 0x01
	viscaTiltDown byte = 0x02
	viscaTiltStop byte = 0x03
)

func viscaPanTiltStop() []byte {
	return viscaPanTilt(0x18, 0x14, viscaPanStop, viscaTiltStop)
}

func viscaPanTiltHome() []byte {
	return []byte{viscaCmdHeader, 0x01, 0x06, 0x04, viscaTerminator}
}

// -----------------------------------------------------------------------------
// Inquiry builders — returns from these come back as viscaReplyCompletion
// with data bytes after the reply header, parsed by the functions below.
// -----------------------------------------------------------------------------

func viscaInqPower() []byte {
	return []byte{viscaCmdHeader, 0x09, 0x04, 0x00, viscaTerminator}
}

func viscaInqZoomPos() []byte {
	return []byte{viscaCmdHeader, 0x09, 0x04, 0x47, viscaTerminator}
}

func viscaInqVersion() []byte {
	return []byte{viscaCmdHeader, 0x09, 0x00, 0x02, viscaTerminator}
}

// parseInqPower reads the payload of a CAM_Power inquiry reply.
// Reply shape: 90 50 <02|03> FF. Returns true for ON.
func parseInqPower(payload []byte) (bool, error) {
	if len(payload) < 4 {
		return false, errors.New("visca: power inquiry reply too short")
	}
	switch payload[2] {
	case 0x02:
		return true, nil
	case 0x03:
		return false, nil
	default:
		return false, fmt.Errorf("visca: unexpected power value 0x%02X", payload[2])
	}
}

// parseInqZoomPos reads a zoom-position inquiry reply.
// Reply shape: 90 50 0p 0q 0r 0s FF → pos = 0xpqrs, 0x0000..0x4000 optical.
func parseInqZoomPos(payload []byte) (uint16, error) {
	if len(payload) < 7 {
		return 0, errors.New("visca: zoom inquiry reply too short")
	}
	pos := uint16(payload[2]&0x0F)<<12 |
		uint16(payload[3]&0x0F)<<8 |
		uint16(payload[4]&0x0F)<<4 |
		uint16(payload[5]&0x0F)
	return pos, nil
}

// parseInqVersion reads the version inquiry reply.
// Reply shape: 90 50 GG GG HH HH JJ JJ KK FF where GG=vendor, HH=model,
// JJ=firmware, KK=? Returns them as a compact "vendor:HHHH fw:JJJJ" string
// so downstream telemetry doesn't need to know the byte layout.
func parseInqVersion(payload []byte) (string, error) {
	if len(payload) < 10 {
		return "", errors.New("visca: version inquiry reply too short")
	}
	vendor := uint16(payload[2])<<8 | uint16(payload[3])
	model := uint16(payload[4])<<8 | uint16(payload[5])
	fw := uint16(payload[6])<<8 | uint16(payload[7])
	return fmt.Sprintf("vendor:%04X model:%04X fw:%04X", vendor, model, fw), nil
}
