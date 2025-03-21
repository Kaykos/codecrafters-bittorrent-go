package main

import (
	"encoding/binary"
	"io"
	"net"
)

const UNCHOKE = uint8(1)
const INTERESTED = uint8(2)
const BITFIELD = uint8(5)
const REQUEST = uint8(6)
const PIECE = uint8(7)
const EXTENSION_MESSAGE = uint8(20)

const HANDSHAKE_MESSAGE_LENGTH = 68

// peerConnection represents the TCP connection with a peer.
type peerConnection struct {
	peerAddress string
	connection  net.Conn
}

// newPeerConnection establishes a TCP connection with the given peerAddress. Returns the connection and the closer
// function to terminate the coneection.
func newPeerConnection(peerAddress string) (*peerConnection, func(), error) {
	// Open TCP connection using peer address
	conn, err := net.Dial("tcp", peerAddress)
	closer := func() {
		conn.Close()
	}

	if err != nil {
		return nil, closer, err
	}

	return &peerConnection{
		peerAddress: peerAddress,
		connection:  conn,
	}, closer, nil
}

// receiveBytes reads the specified number of bytes from the peer connection and returns the slice of bytes read.
func (pc *peerConnection) receiveBytes(size int) ([]byte, error) {
	buf := make([]byte, size)

	_, err := io.ReadFull(pc.connection, buf)
	if err != nil {
		return nil, err
	}

	return buf, nil
}

// receivePeerMessage reads from the peer connection and builds a new peerMessage.
func (pc *peerConnection) receivePeerMessage() (*peerMessage, error) {
	// Read only 4 bytes to figure out message length
	buf, err := pc.receiveBytes(4)
	if err != nil {
		return nil, err
	}

	msgLength := binary.BigEndian.Uint32(buf)

	// Build the message buffer, using the known length
	msgBuf, err := pc.receiveBytes(int(msgLength))
	if err != nil {
		return nil, err
	}

	return newPeerMessage(msgBuf), nil
}

// sendMessage writes bytes into the peer connection.
func (pc *peerConnection) sendBytes(message []byte) (int, error) {
	return pc.connection.Write(message)
}

// sendMessage writes a message into the peer connection.
func (pc *peerConnection) sendMessage(message peerMessage) (int, error) {
	return pc.connection.Write(message.bytes())
}

// peerMessage represents the messages transmitted between peers.
type peerMessage struct {
	length  uint32 // 4 byte integer indicating the length of the message (type + payload)
	mType   uint8  // 1 byte integer specifies the type of the message
	payload []byte
}

// bytes returns the byte representation of a peerMessage, used to transmit the message.
func (m *peerMessage) bytes() []byte {
	// Buffer length adds 4 to account for the length prefix
	bytes := make([]byte, 0, m.length+4)

	bytes = binary.BigEndian.AppendUint32(bytes, m.length) // Message length prefix: 4 bytes
	bytes = append(bytes, m.mType)
	bytes = append(bytes, m.payload...)

	return bytes
}

// newPeerMessage builds a peerMessage from a slice of bytes.
func newPeerMessage(b []byte) *peerMessage {
	payload := b[1:]
	length := len(payload) + 1

	return &peerMessage{
		length:  uint32(length), // Message length is the length of the payload + 1 byte for the message type
		mType:   b[0],           // Message type is in the first byte
		payload: payload,
	}
}

// buildHandshakeMessage returns the byte slice needed for handshake
func buildHandshakeMessage(peerId, infoHash []byte, supportExtensions bool) []byte {
	message := make([]byte, 0, HANDSHAKE_MESSAGE_LENGTH)

	message = append(message, byte(19))                         // First byte indicates the length of the protocol string
	message = append(message, []byte("BitTorrent protocol")...) // Protocol string (19 bytes)
	reservedBytes := make([]byte, 8)                            // Eight reserved bytes, set to 0
	if supportExtensions {
		// If our client supports extensions, the 20th bit from the right (count starting in 0, from the total 64 reserved bits) is set to 1
		// This sets the byte to 00010000, which is 16 in decimal
		reservedBytes[5] = 16
	}

	message = append(message, reservedBytes...)
	message = append(message, infoHash...) // 20 bytes for info hash
	message = append(message, peerId...)   // 20 bytes for random peer id

	return message
}

func buildInterestedMessage() peerMessage {
	return peerMessage{
		length: uint32(1),
		mType:  INTERESTED,
	}
}

func buildRequestMessage(pieceIndex, begin, blockLength int) peerMessage {
	// 12 bytes payload: 3 4-byte integers
	payload := make([]byte, 0, 12)

	payload = binary.BigEndian.AppendUint32(payload, uint32(pieceIndex))
	payload = binary.BigEndian.AppendUint32(payload, uint32(begin))
	payload = binary.BigEndian.AppendUint32(payload, uint32(blockLength))

	return peerMessage{
		length:  uint32(13), // Payload length + 1 byte for mType
		mType:   REQUEST,
		payload: payload,
	}
}

const METADATA_EXTENSTION_REQUEST = 0
const METADATA_EXTENSTION_DATA = 1
const METADATA_EXTENSTION_REJECT = 2

func buildExtensionHandshakeMessage() peerMessage {
	messagePayload := map[string]any{
		"m": map[string]any{
			"ut_metadata": 123,
		},
	}
	// d1:md11:ut_metadatai123eee

	var payload []byte

	payload = append(payload, byte(0))
	payload = append(payload, []byte(bencodeMap(messagePayload))...)

	return peerMessage{
		length:  uint32(len(payload)) + 1,
		mType:   EXTENSION_MESSAGE,
		payload: payload,
	}
}

func buildMetadataRequestMessage(metadataExtensionId int) peerMessage {
	messagePayload := map[string]any{
		"msg_type": METADATA_EXTENSTION_REQUEST,
		"piece":    0, // Zero-based page index, we'll always be requesting just one page, so always 0
	}

	var payload []byte

	payload = append(payload, byte(metadataExtensionId))
	payload = append(payload, []byte(bencodeMap(messagePayload))...)

	return peerMessage{
		length:  uint32(len(payload)) + 1,
		mType:   EXTENSION_MESSAGE,
		payload: payload,
	}
}
