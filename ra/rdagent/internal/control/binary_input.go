package control

import (
	"encoding/binary"
	"fmt"
	"math"
)

type BinaryInputKind uint8

const (
	BinaryInputMouseMove BinaryInputKind = 1
	BinaryInputMouseDown BinaryInputKind = 2
	BinaryInputMouseUp   BinaryInputKind = 3
	BinaryInputWheel     BinaryInputKind = 4
	BinaryInputKeyDown   BinaryInputKind = 5
	BinaryInputKeyUp     BinaryInputKind = 6
)

// Binary input protocol v1.
//
// Transport/framing:
//   - WebRTC DataChannel message boundary is the frame.
//   - Binary messages are sent as ArrayBuffer from viewer.
//   - JSON messages are sent as string from viewer.
//   - Agent dispatches by DataChannelMessage.IsString before JSON parsing.
//
// Byte order:
//   - Little endian for all multi-byte fields.
//
// Coordinates:
//   - x/y are normalized video coordinates encoded as uint16.
//   - 0     => 0.0
//   - 65535 => 1.0
//
// Layout:
//   MouseMove:       u8 kind, u16 x, u16 y
//   MouseDown/Up:    u8 kind, u16 x, u16 y, u8 button
//   Wheel:           u8 kind, u16 x, u16 y, i32 delta_x, i32 delta_y
//   KeyDown/KeyUp:   u8 kind, u16 vk, u16 modifiers
//
// Button:
//   1 left, 2 middle, 3 right.
//
// Modifiers are reserved for future use.
// Current injector tracks key state itself and does not need modifiers.

type BinaryInput struct {
	Kind      BinaryInputKind
	X         float64
	Y         float64
	Button    string
	DeltaX    int32
	DeltaY    int32
	VK        uint16
	Modifiers uint16
}

func DecodeBinaryInput(raw []byte) (BinaryInput, error) {
	if len(raw) == 0 {
		return BinaryInput{}, fmt.Errorf("empty binary input message")
	}

	kind := BinaryInputKind(raw[0])

	switch kind {
	case BinaryInputMouseMove:
		if len(raw) != 5 {
			return BinaryInput{}, fmt.Errorf("invalid mouse_move binary size: %d", len(raw))
		}

		return BinaryInput{
			Kind: kind,
			X:    decodeNorm16(raw[1:3]),
			Y:    decodeNorm16(raw[3:5]),
		}, nil

	case BinaryInputMouseDown, BinaryInputMouseUp:
		if len(raw) != 6 {
			return BinaryInput{}, fmt.Errorf("invalid mouse_button binary size: %d", len(raw))
		}

		button, ok := decodeButton(raw[5])
		if !ok {
			return BinaryInput{}, fmt.Errorf("invalid mouse button: %d", raw[5])
		}

		return BinaryInput{
			Kind:   kind,
			X:      decodeNorm16(raw[1:3]),
			Y:      decodeNorm16(raw[3:5]),
			Button: button,
		}, nil

	case BinaryInputWheel:
		if len(raw) != 13 {
			return BinaryInput{}, fmt.Errorf("invalid wheel binary size: %d", len(raw))
		}

		return BinaryInput{
			Kind:   kind,
			X:      decodeNorm16(raw[1:3]),
			Y:      decodeNorm16(raw[3:5]),
			DeltaX: int32(binary.LittleEndian.Uint32(raw[5:9])),
			DeltaY: int32(binary.LittleEndian.Uint32(raw[9:13])),
		}, nil

	case BinaryInputKeyDown, BinaryInputKeyUp:
		if len(raw) != 5 {
			return BinaryInput{}, fmt.Errorf("invalid key binary size: %d", len(raw))
		}

		return BinaryInput{
			Kind:      kind,
			VK:        binary.LittleEndian.Uint16(raw[1:3]),
			Modifiers: binary.LittleEndian.Uint16(raw[3:5]),
		}, nil

	default:
		return BinaryInput{}, fmt.Errorf("unknown binary input kind: %d", kind)
	}
}

func decodeNorm16(raw []byte) float64 {
	value := binary.LittleEndian.Uint16(raw)
	return math.Max(0, math.Min(1, float64(value)/65535.0))
}

func decodeButton(value uint8) (string, bool) {
	switch value {
	case 1:
		return "left", true
	case 2:
		return "middle", true
	case 3:
		return "right", true
	default:
		return "", false
	}
}
