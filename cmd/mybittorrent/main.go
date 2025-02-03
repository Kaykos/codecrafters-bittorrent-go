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
	mathRand "math/rand"
	"net/http"
	"net/url"
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
func (t torrent) handshake(conn *peerConnection) ([]byte, error) {
	peerId := make([]byte, 20)
	rand.Read(peerId)

	// Send handshake message
	message := buildHandshakeMessage(peerId, t.infoHash)
	_, err := conn.sendMessage(message)
	if err != nil {
		return nil, err
	}

	// Receive handshake response
	res, err := conn.receiveBytes(HANDSHAKE_MESSAGE_LENGTH)
	if err != nil {
		return nil, err
	}

	return res, nil
}

func (t torrent) peerHandshake(peer string) (string, error) {
	conn, closer, err := newPeerConnection(peer)
	if err != nil {
		return "", err
	}
	defer closer()

	res, err := t.handshake(conn)

	if err != nil {
		return "", err
	}

	// Received message has identical structure to the one sent
	// Get the peer id from the last 20 bytes of the message
	peerId := res[48:]
	return toHex(peerId), nil
}

// getPieceFromPeer downloads the piece defined by pieceIndex
func (t torrent) getPieceFromPeer(conn *peerConnection, pieceIndex int, waitInitialMessages bool) ([]byte, error) {
	if waitInitialMessages {
		// Receive bitfield message
		//fmt.Println("  Waiting for bitfield...")
		bitfield, err := conn.receivePeerMessage()
		if err != nil {
			return nil, err
		}
		if bitfield.mType != BITFIELD {
			return nil, fmt.Errorf("received unexpected message type. Expected bitfield(%d), received: %d", BITFIELD, bitfield.mType)
		}

		// Send interested message
		interestedMessage := buildInterestedMessage()
		_, err = conn.sendMessage(interestedMessage.bytes())

		// Receive unchoke message
		//fmt.Println("  Waiting for unchoke...")
		unchoke, err := conn.receivePeerMessage()
		if err != nil {
			return nil, err
		}
		if unchoke.mType != UNCHOKE {
			return nil, fmt.Errorf("received unexpected message type. Expected unchoke(%d), received: %d", UNCHOKE, unchoke.mType)
		}
	}

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
			// All message requests will ask for exactly blockSize bytes, except the last one which most likely ask for
			// the remaining amount of bytes
			blockLength = pieceLength - begin
		}

		requestMessage := buildRequestMessage(pieceIndex, begin, blockLength)
		fmt.Printf(" Requesting block %d with block length: %d\n", i, blockLength)
		_, err := conn.sendMessage(requestMessage.bytes())
		if err != nil {
			return nil, err
		}

		// Receive piece message
		//fmt.Println("  Waiting for piece...")
		piece, err := conn.receivePeerMessage()
		if err != nil {
			return nil, err
		}

		if piece.mType != PIECE {
			return nil, fmt.Errorf("received unexpected message type. Expected piece(%d), received: %d", PIECE, piece.mType)
		}
		fmt.Printf(" Received piece message for block %d\n", i)

		// Piece message payload is: 4 bytes for index. 4 bytes for begin. Rest of the bytes are the piece data
		// Ignore the first 8 bytes, and only use the actual piece data
		pieceData = append(pieceData, piece.payload[8:]...)
		fmt.Println()
	}

	return pieceData, nil
}

func (t torrent) downloadPieceToFile(outputPath string, pieceIndex int) {
	peerAddresses, err := t.peers()
	if err != nil {
		fmt.Println(err)
		return
	}

	// Pick a random peer
	address := peerAddresses[mathRand.Intn(len(peerAddresses))]

	conn, closer, err := newPeerConnection(address)
	if err != nil {
		fmt.Println(err)
	}
	defer closer() // Close peer connection

	// Send handshake
	_, err = t.handshake(conn)
	if err != nil {
		fmt.Println(err)
	}

	// Get piece data
	pieceData, err := t.getPieceFromPeer(conn, pieceIndex, true)

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

func (t torrent) downloadFile(outputPath string) {
	peers, _ := t.peers()

	connections := make(map[string]*peerConnection, len(peers))
	closerFuncs := make([]func(), 0, len(peers))

	defer func() {
		// Execute all closer functions
		for _, c := range closerFuncs {
			c()
		}
	}()

	fileData := make([]byte, 0, t.info.length)

	for pieceIndex, pieceHash := range t.info.pieces {
		address := peers[mathRand.Intn(len(peers))]
		fmt.Printf("Downloading piece %d from peer %s\n", pieceIndex, address)

		conn, ok := connections[address]

		if !ok {
			// Create connection if we haven't done yet
			newConn, closer, err := newPeerConnection(address)
			if err != nil {
				fmt.Println(err)
				return
			}
			conn = newConn
			connections[address] = conn
			// Add closer function
			closerFuncs = append(closerFuncs, closer)

			// Send handshake
			_, err = t.handshake(conn)
			if err != nil {
				fmt.Println(err)
			}
		}

		// Get piece data
		// If connection already exists (we had downloaded a piece from that peer),
		// skip the initial messages: bitfield, interested, unchoke
		pieceData, err := t.getPieceFromPeer(conn, pieceIndex, !ok)
		if err != nil {
			fmt.Println(err)
			return
		}

		expectedHash := toHex(pieceHash)
		fmt.Printf("Expected piece hash:    %s\n", expectedHash)

		h := sha1.New()
		h.Write(pieceData)
		writtenPieceHash := toHex(h.Sum(nil))
		fmt.Printf("Downloaded piece hash:  %s\n", writtenPieceHash)

		if expectedHash != writtenPieceHash {
			fmt.Printf(" !! Piece hashes do not mash. Terminating")
			return
		}

		fileData = append(fileData, pieceData...)
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
	n, err := file.Write(fileData)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("\nWrote %d bytes to %s \n", n, outputPath)
}

func parseMagnetLink(link string) {
	// Link starts with: 'magnet:?'
	queryParameters, err := url.ParseQuery(link[8:])
	if err != nil {
		fmt.Println(err)
		return
	}

	tracker := queryParameters.Get("tr")
	// xt starts with: 'urn:btih:'
	infoHash := queryParameters.Get("xt")[9:]

	fmt.Printf("Tracker URL: %s\nInfo Hash: %s\n", tracker, infoHash)
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

		torrent.downloadPieceToFile(output, pieceIndex)
	} else if command == "download" {
		flag := os.Args[2]
		if flag != "-o" {
			fmt.Println("Missing output flag: '-o'")
			return
		}

		output := os.Args[3]
		file := os.Args[4]

		torrent, err := parseTorrentFile(file)
		if err != nil {
			fmt.Println(err)
			return
		}

		torrent.downloadFile(output)
	} else if command == "magnet_parse" {
		magnetLink := os.Args[2]
		parseMagnetLink(magnetLink)
	} else {
		fmt.Println("Unknown command: " + command)
		os.Exit(1)
	}
}
