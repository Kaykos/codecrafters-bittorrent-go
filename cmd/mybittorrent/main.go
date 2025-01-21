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
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	// bencode "github.com/jackpal/bencode-go" // Available if you need it!
)

type info struct {
	length      int
	name        string
	nPieces     int
	pieceLength int
	pieces      [][]byte
}

type torrent struct {
	announce string
	info     info
	infoHash []byte
}

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
		nPieces:     n,
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
func (t torrent) handshake(peer string) ([]byte, *peerConnection, func(), error) {
	peerId := make([]byte, 20)
	rand.Read(peerId)

	peerConn, closer, err := newPeerConnection(peer)
	if err != nil {
		return nil, nil, nil, err
	}

	// Send handshake message
	message := buildHandshakeMessage(peerId, t.infoHash)
	_, err = peerConn.sendMessage(message)
	if err != nil {
		return nil, nil, nil, err
	}

	// Receive handshake response
	res, err := peerConn.receiveBytes(HANDSHAKE_MESSAGE_LENGTH)
	if err != nil {
		return nil, nil, nil, err
	}

	return res, peerConn, closer, nil
}

func (t torrent) peerHandshake(peer string) (string, error) {
	res, _, closer, err := t.handshake(peer)
	defer closer()

	if err != nil {
		return "", err
	}

	// Received message has identical structure to the one sent
	// Get the peer id from the last 20 bytes of the message
	peerId := res[48:]
	return toHex(peerId), nil
}

// downloadPiece downloads the piece defined by pieceIndex into the given path.
func (t torrent) downloadPiece(outputPath string, pieceIndex int) {
	peerAddresses, err := t.peers()
	if err != nil {
		fmt.Println(err)
		return
	}

	// TODO Proper peer selection
	address := peerAddresses[0]

	fmt.Printf("Downloading piece %d from peer %s\n", pieceIndex, address)

	_, peerConn, closer, err := t.handshake(address)
	defer closer()
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Printf("Established connection with peer\n")

	// Receive bitfield message
	bitfield, err := peerConn.receivePeerMessage()
	if bitfield.mType != BITFIELD {
		fmt.Printf(" !! Received unexpected message type. Expected bitfield(%d), received: %d\n", BITFIELD, bitfield.mType)
		return
	}

	// Send interested message
	interestedMessage := buildInterestedMessage()
	_, err = peerConn.sendMessage(interestedMessage.bytes())

	// Receive ubnchoke message
	unchoke, err := peerConn.receivePeerMessage()
	if unchoke.mType != UNCHOKE {
		fmt.Printf(" !! Received unexpected message type. Expected unchoke(%d), received: %d\n", UNCHOKE, unchoke.mType)
		return
	}

	fmt.Println(t.infoStr())

	pieceLength := t.info.pieceLength

	// When processing the last piece, the piece length is lower than the predefined pieceLength
	if pieceIndex == t.info.nPieces-1 {
		pieceLength = t.info.length % t.info.pieceLength
	}

	// Max block size is 2^14 = 16_384
	blockSize := 16_384
	nBlocks := int(math.Ceil(float64(pieceLength) / float64(blockSize)))

	// Buffer to keep all the piece data
	pieceData := make([]byte, 0, pieceLength)

	fmt.Printf("Piece will be divided in %d blocks\n", nBlocks+1)

	for i := 0; i < nBlocks; i++ {
		begin := i * blockSize
		blockLength := blockSize
		if i == nBlocks-1 {
			// All message requests will ask for exaclty blockSize bytes, except the last one which most likely ask for
			// the remaining amount of bytes
			blockLength = pieceLength - begin
		}

		requestMessage := buildRequestMessage(pieceIndex, begin, blockLength)
		fmt.Printf(" Requesting block %d with block length: %d\n", i, blockLength)
		_, err := peerConn.sendMessage(requestMessage.bytes())
		if err != nil {
			fmt.Println(err)
			return
		}

		piece, err := peerConn.receivePeerMessage()
		if err != nil {
			fmt.Println(err)
			return
		}

		if piece.mType != PIECE {
			fmt.Printf(" !! Received unexpected message type. Expected piece(%d), received: %d\n", PIECE, piece.mType)
			return
		}
		fmt.Printf(" Received piece message for block %d\n", i)

		// Piece message payload is: 4 bytes for index. 4 bytes for begin. Rest of the bytes are the piece data
		// Ignore the first 8 bytes, and only use the actual piece data
		pieceData = append(pieceData, piece.payload[8:]...)
		fmt.Println()
	}

	expectedHash := toHex(t.info.pieces[pieceIndex])
	fmt.Printf("Expected piece hash: %s\n", expectedHash)

	h := sha1.New()
	h.Write(pieceData)
	writtenPieceHash := toHex(h.Sum(nil))
	fmt.Printf("Written piece hash:  %s\n", writtenPieceHash)

	if expectedHash != writtenPieceHash {
		fmt.Printf(" !! Piece hashes do not mash. Terminating")
		return
	}

	// Create subfolder if outputPath has it
	if err := os.MkdirAll(filepath.Dir(outputPath), 0770); err != nil {
		fmt.Printf(" !! Could not create output directory: %s\n", err)
		return
	}

	file, err := os.Create(outputPath)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer file.Close()
	n, err := file.Write(pieceData)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("\nWrote %d bytes to %s \n", n, outputPath)
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

		peerId, err := torrent.peerHandshake(peerAddress)
		if err != nil {
			fmt.Println(err)
			return
		}

		fmt.Printf("Peer ID: %s\n", peerId)
	} else if command == "download_piece" {
		flag := os.Args[2]
		if flag != "-o" {
			fmt.Println("Missing output flag: '-o'")
			return
		}

		output := os.Args[3]
		file := os.Args[4]
		pieceIndex, err := strconv.Atoi(os.Args[5])
		if err != nil {
			fmt.Println(err)
			return
		}

		torrent, err := parseTorrentFile(file)
		if err != nil {
			fmt.Println(err)
			return
		}

		torrent.downloadPiece(output, pieceIndex)
	} else {
		fmt.Println("Unknown command: " + command)
		os.Exit(1)
	}
}
