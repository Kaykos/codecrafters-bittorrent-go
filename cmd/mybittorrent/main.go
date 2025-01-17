package main

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	// bencode "github.com/jackpal/bencode-go" // Available if you need it!
)

type info struct {
	length      int
	name        string
	pieceLength int
	pieces      [][]byte
}

type torrent struct {
	announce string
	info     info
	infoHash []byte
}

// Ensures gofmt doesn't remove the "os" encoding/json import (feel free to remove this!)
var _ = json.Marshal

// buildPeerAddresses uses the peers string returned by the tracker to build a slice of strings containing the peer
// addresses
func buildPeerAddresses(peersStr string) []string {
	// Each peer is represented using 6 bytes. 4 bytes for the IP, and 2 for the port
	const length = 6

	n := len(peersStr) / length

	peerAddresses := make([]string, 0, n)

	for i := 0; i < n; i++ {
		peer := peersStr[i*length : i*length+length]

		ipSlice := []byte(peer[:4])
		portSlice := []byte(peer[4:])

		ip := fmt.Sprintf("%d.%d.%d.%d", ipSlice[0], ipSlice[1], ipSlice[2], ipSlice[3])
		port := binary.BigEndian.Uint16(portSlice)

		peerAddresses = append(peerAddresses, fmt.Sprintf("%s:%d", ip, port))
	}

	return peerAddresses
}

// peersQueryParams builds the query parameters needed to execute the peers request. Returns
// a string containing the URL encoded query parameters
func peersQueryParams(t torrent, req *http.Request) (string, error) {
	q := req.URL.Query()
	q.Add("info_hash", string(t.infoHash))
	q.Add("peer_id", "kaykos-go-bittorrent")
	q.Add("port", "6881")
	q.Add("uploaded", "0")
	q.Add("downloaded", "0")
	q.Add("left", strconv.Itoa(t.info.length))
	q.Add("compact", "1")

	return q.Encode(), nil
}

func parseTorrentFile(filename string) (torrent, error) {
	t := torrent{}

	file, err := os.Open(filename)
	if err != nil {
		return t, err
	}

	defer file.Close()

	fileContent, err := io.ReadAll(file)
	if err != nil {
		return t, err
	}

	torrentDict, _, err := decodeDictionary(string(fileContent))
	if err != nil {
		return t, err
	}

	infoDict := torrentDict["info"].(map[string]any)
	piecesStr := infoDict["pieces"].(string)

	n := len(piecesStr) / 20
	pieces := make([][]byte, n)

	for i := 0; i < n; i++ {
		pieceStr := piecesStr[i*20 : (i+1)*20]
		pieces[i] = []byte(pieceStr)
	}

	t.info = info{
		length:      infoDict["length"].(int),
		name:        infoDict["name"].(string),
		pieceLength: infoDict["piece length"].(int),
		pieces:      pieces,
	}

	t.announce = torrentDict["announce"].(string)
	t.infoHash = infoHash(infoDict)

	return t, nil
}

// infoStr returns a string representing a summary of the torrent file
func (t torrent) infoStr() string {
	hexInfoHash := toHex(t.infoHash)
	hexPieceHashes := make([]string, len(t.info.pieces))

	for i, pieceHash := range t.info.pieces {
		hexPieceHashes[i] = toHex(pieceHash)
	}
	hashPiecesStr := strings.Join(hexPieceHashes, "\n")

	return fmt.Sprintf("Tracker URL: %s\nLength: %d\nInfo Hash: %s\nPiece Length: %d\nPiece Hashes:\n%s",
		t.announce, t.info.length, hexInfoHash, t.info.pieceLength, hashPiecesStr)
}

// peers returns a slice of strings containing the peers of torrent. This is done by requesting the tracker and parsing
// the response to build IP and port for each peer
func (t torrent) peers() ([]string, error) {
	client := &http.Client{
		Timeout: time.Second * 10,
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, t.announce, nil)
	if err != nil {
		return nil, err
	}

	queryParams, err := peersQueryParams(t, req)
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = queryParams

	res, err := client.Do(req)
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, errors.New(res.Status)
	}

	resContent, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	decodedRes, _, err := decodeDictionary(string(resContent))
	if err != nil {
		return nil, err
	}

	peersStr, ok := decodedRes["peers"].(string)
	if !ok {
		return nil, errors.New("in response body 'peers' must be a string")
	}

	return buildPeerAddresses(peersStr), nil
}

// infoHash bencodes the info map and returns the SHA-1 hash string representation
func infoHash(info map[string]any) []byte {
	infoStr := bencodeMap(info)

	h := sha1.New()
	h.Write([]byte(infoStr))

	return h.Sum(nil)
}

func toHex(b []byte) string {
	return hex.EncodeToString(b)
}

// handshake sends initial handshake message to the given peer. Returns a []byte that has the peer ID taken from the
// response message sent by the peer
func (t torrent) handshake(peer string) ([]byte, error) {
	const messageSize = 68

	message := make([]byte, 0, messageSize)
	res := make([]byte, messageSize)

	peerId := make([]byte, 20)
	rand.Read(peerId)

	message = append(message, byte(19))                         // First byte indicates the length of the protocol string
	message = append(message, []byte("BitTorrent protocol")...) // Protocol string (19 bytes)
	message = append(message, make([]byte, 8)...)               // Eight reserved bytes, set to 0
	message = append(message, t.infoHash...)                    // 20 bytes for info hash
	message = append(message, peerId...)                        // 20 bytes for random peer id

	// Open TCP connection using peer address
	conn, err := net.Dial("tcp", peer)
	if err != nil {
		return nil, err
	}

	// Write handshake message
	_, err = conn.Write(message)
	if err != nil {
		return nil, err
	}

	defer conn.Close()

	// Read handshake response
	_, err = io.ReadFull(conn, res)
	if err != nil {
		return nil, err
	}

	// Get the peer id from the last 20 bytes of the message
	return res[48:], nil
}

func main() {
	command := os.Args[1]
	//command = "info"

	if command == "decode" {
		bencodedValue := os.Args[2]

		decoded, _, err := decodeValue(bencodedValue)
		if err != nil {
			fmt.Println(err)
			return
		}

		jsonOutput, _ := json.Marshal(decoded)
		fmt.Println(string(jsonOutput))
	} else if command == "info" {
		file := os.Args[2]

		torrent, err := parseTorrentFile(file)
		if err != nil {
			fmt.Println(err)
			return
		}

		fmt.Println(torrent.infoStr())
	} else if command == "peers" {
		file := os.Args[2]

		torrent, err := parseTorrentFile(file)
		if err != nil {
			fmt.Println(err)
			return
		}

		peerAddresses, err := torrent.peers()
		if err != nil {
			fmt.Println(err)
			return
		}
		for _, peer := range peerAddresses {
			fmt.Println(peer)
		}
	} else if command == "handshake" {
		file := os.Args[2]
		peerAddress := os.Args[3]

		torrent, err := parseTorrentFile(file)
		if err != nil {
			fmt.Println(err)
			return
		}

		peerId, err := torrent.handshake(peerAddress)
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Printf("Peer ID: %s\n", toHex(peerId))
	} else {
		fmt.Println("Unknown command: " + command)
		os.Exit(1)
	}
}
