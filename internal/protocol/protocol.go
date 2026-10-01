package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const (
	KiB                 = 1 << 10
	HeaderSizeBytes     = 4
	MaxPayloadSizeBytes = 10 * KiB // 10 KiB
)

// Version is the protocol version spoken by this build. The server
// rejects agents that register with a different version.
const Version = 1

const (
	KindRegister   = "REGISTER"
	KindOk         = "OK"
	KindError      = "ERROR"
	KindStreamOpen = "STREAM_OPEN"
)

type Envelope struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// RegisterMessage agent -> server
type RegisterMessage struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	Token   string `json:"token,omitempty"`
}

// OkMessage server -> agent
type OkMessage struct {
	Name       string `json:"name"`
	PublicPort uint32 `json:"public_port"`
}

// ErrorCode classifies a rejected registration so the agent can decide
// whether retrying can help.
type ErrorCode string

const (
	// ErrBadRequest: the agent sent a malformed or unexpected message.
	ErrBadRequest ErrorCode = "bad_request"
	// ErrUnsupportedVersion: the agent's protocol version is not supported.
	ErrUnsupportedVersion ErrorCode = "unsupported_version"
	// ErrUnauthorized: the agent's token was missing or wrong.
	ErrUnauthorized ErrorCode = "unauthorized"
	// ErrNoCapacity: the server has no public port to offer right now.
	ErrNoCapacity ErrorCode = "no_capacity"
)

// Retryable reports whether the same registration may succeed later.
// Unknown codes are treated as retryable.
func (c ErrorCode) Retryable() bool {
	switch c {
	case ErrBadRequest, ErrUnsupportedVersion, ErrUnauthorized:
		return false
	default:
		return true
	}
}

// ErrorMessage server -> agent
type ErrorMessage struct {
	Code   ErrorCode `json:"code"`
	Reason string    `json:"reason"`
}

// StreamOpenMessage server -> agent, sent as the first frame of every
// tunnelled stream. The tunnelled bytes follow it directly.
type StreamOpenMessage struct {
	// ClientAddr is the address of the public client that connected.
	ClientAddr string `json:"client_addr"`
	// PublicAddr is the server address the client connected to.
	PublicAddr string `json:"public_addr"`
}

func decodePayload[T any](payload []byte) (any, error) {
	var target T

	if err := json.Unmarshal(payload, &target); err != nil {
		return nil, err
	}

	return target, nil
}

var registry = map[string]func([]byte) (any, error){
	KindRegister:   decodePayload[RegisterMessage],
	KindOk:         decodePayload[OkMessage],
	KindError:      decodePayload[ErrorMessage],
	KindStreamOpen: decodePayload[StreamOpenMessage],
}

func WriteMessage(w io.Writer, kind string, payload any) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	env := Envelope{
		Kind:    kind,
		Payload: payloadBytes,
	}

	envBytes, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("failed to marshal envelope: %w", err)
	}

	if len(envBytes) > MaxPayloadSizeBytes {
		return fmt.Errorf("outgoing payload size (%d bytes) exceeds maximum limit (%d bytes)", len(envBytes), MaxPayloadSizeBytes)
	}

	msgLength := uint32(len(envBytes))
	header := make([]byte, HeaderSizeBytes)
	binary.BigEndian.PutUint32(header, msgLength)

	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("failed to write message header: %w", err)
	}

	if _, err := w.Write(envBytes); err != nil {
		return fmt.Errorf("failed to write message body: %w", err)
	}

	return nil
}

func ReadMessage(r io.Reader) (any, error) {
	header := make([]byte, HeaderSizeBytes)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint32(header)
	if length > MaxPayloadSizeBytes {
		return nil, fmt.Errorf("incoming payload size (%d bytes) exceeds max limit (%d bytes)", length, MaxPayloadSizeBytes)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("failed to read full payload (expected %d bytes): %w", length, err)
	}

	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("failed to unmarshall envelope: %w", err)
	}

	decoder, exists := registry[env.Kind]
	if !exists {
		return nil, fmt.Errorf("unsupported message kind: %s", env.Kind)
	}

	return decoder(env.Payload)
}
