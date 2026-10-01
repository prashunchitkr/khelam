package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const (
	KiB                 = 1 << 10
	HeaderSizeBytes     = 4
	MaxPayloadSizeBytes = 10 * KiB // 10 KiB
)

// Version is the protocol version spoken by this build. The server
// rejects agents that register with a different version.
const Version = 2

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

// Protocol is the transport protocol of a tunnel.
type Protocol string

const (
	ProtocolTCP Protocol = "tcp"
	ProtocolUDP Protocol = "udp"
)

// TunnelRequest asks the server to expose one local service.
type TunnelRequest struct {
	// Name is unique per agent and keys the agent's sticky port.
	Name     string   `json:"name"`
	Protocol Protocol `json:"protocol"`
}

// RegisterMessage agent -> server, the first message on the control
// stream.
type RegisterMessage struct {
	Version int `json:"version"`
	// AgentID is a stable random identifier for this agent install.
	AgentID string          `json:"agent_id"`
	Name    string          `json:"name"`
	Token   string          `json:"token,omitempty"`
	Tunnels []TunnelRequest `json:"tunnels"`
}

// TunnelAssignment describes a tunnel the server has opened.
type TunnelAssignment struct {
	// ID identifies the tunnel in stream headers and datagrams.
	ID         uint32   `json:"id"`
	Name       string   `json:"name"`
	Protocol   Protocol `json:"protocol"`
	PublicPort uint32   `json:"public_port"`
}

// OkMessage server -> agent, listing tunnels in request order.
type OkMessage struct {
	Tunnels []TunnelAssignment `json:"tunnels"`
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
	// TunnelID is the tunnel the client connected to.
	TunnelID uint32 `json:"tunnel_id"`
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

// UDPFlowIdleTimeout is how long a UDP flow may stay silent in both
// directions before the server and agent forget it.
const UDPFlowIdleTimeout = 60 * time.Second

// DatagramHeaderSize is the length of the header in front of every
// tunnelled UDP payload.
const DatagramHeaderSize = 8

// AppendDatagram appends a tunnelled UDP datagram to dst. flowID
// identifies one public client of the tunnel.
func AppendDatagram(dst []byte, tunnelID, flowID uint32, payload []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, tunnelID)
	dst = binary.BigEndian.AppendUint32(dst, flowID)
	return append(dst, payload...)
}

// ParseDatagram splits a tunnelled UDP datagram into its parts. payload
// aliases b.
func ParseDatagram(b []byte) (tunnelID, flowID uint32, payload []byte, err error) {
	if len(b) < DatagramHeaderSize {
		return 0, 0, nil, fmt.Errorf("datagram too short (%d bytes)", len(b))
	}
	return binary.BigEndian.Uint32(b[0:4]), binary.BigEndian.Uint32(b[4:8]), b[DatagramHeaderSize:], nil
}
