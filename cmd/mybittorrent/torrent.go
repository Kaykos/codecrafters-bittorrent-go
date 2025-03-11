package main

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	mathRand "math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type torrent struct {
	announce string
	info     info
	infoHash []byte
}

type info struct {
	length      int
	name        string
	nPieces     int
	pieceLength int
	pieces      [][]byte
}

// parseTorrentFile creates a torrent instance from the given filename
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

// parseMagnetLink creates a torrent instance from a magnet link
func parseMagnetLink(link string) (torrent, error) {
	t := torrent{}

	// Example link: magnet:?xt=urn:btih:ad42ce8109f54c99613ce38f9b4d87e70f24a165&dn=magnet1.gif&tr=http%3A%2F%2Fbittorrent-test-tracker.codecrafters.io%2Fannounce
	// Link starts with 'magnet:?', parse the link from there
	queryParameters, err := url.ParseQuery(link[8:])
	if err != nil {
		return t, err
	}

	t.announce = queryParameters.Get("tr")
	// xt starts with: 'urn:btih:'
	hexInfoHash := queryParameters.Get("xt")[9:]
	t.infoHash, err = hex.DecodeString(hexInfoHash)
	t.info.name = queryParameters.Get("dn")

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

// peers returns a slice of strings containing the peer addresses of torrent. This is done by requesting the tracker and parsing
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

// handshake sends initial handshake message to the given peer. Returns a the raw response returned by the peer
func (t torrent) handshake(conn *peerConnection, supportExtensions bool) ([]byte, error) {
	peerId := make([]byte, 20)
	rand.Read(peerId)

	// Send handshake message
	message := buildHandshakeMessage(peerId, t.infoHash, supportExtensions)
	_, err := conn.sendBytes(message)
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

// peerHandshake sends the initial message to a peer. Returns the hexadecimal representation of the response peer ID
func (t torrent) peerHandshake(peer string, supportExtensions bool) (string, error) {
	conn, closer, err := newPeerConnection(peer)
	if err != nil {
		return "", err
	}
	defer closer()

	res, err := t.handshake(conn, supportExtensions)

	if err != nil {
		return "", err
	}

	// Received message has identical structure to the one sent
	// Get the peer id from the last 20 bytes of the message
	peerId := res[48:]
	return toHex(peerId), nil
}

func (t torrent) magnetHandshake() (string, int, error) {
	var peerId string
	var peerMetadataExtensionId int

	peers, err := t.peers()
	if err != nil {
		return peerId, peerMetadataExtensionId, err
	}

	peer := peers[0]

	conn, closer, err := newPeerConnection(peer)
	defer closer()

	// Traditional handshake
	res, err := t.handshake(conn, true)
	if err != nil {
		return peerId, peerMetadataExtensionId, err
	}

	// Receive bitfield
	_, err = conn.receivePeerMessage()
	if err != nil {
		return peerId, peerMetadataExtensionId, err
	}

	// Just as the handshake message sent, the received message has 8 reserved bytes
	// If the peer supports extensions, the 6 byte is set to 16
	peerSupportsExtensions := res[25] == 16
	if peerSupportsExtensions {
		// If the peer handles extensions, send extension handshake
		extensionHandshake := buildExtensionHandshakeMessage()
		_, err := conn.sendMessage(extensionHandshake)
		if err != nil {
			return peerId, peerMetadataExtensionId, err
		}

		// Receive extension handshake response
		resHandshake, err := conn.receivePeerMessage()
		if err != nil {
			return peerId, peerMetadataExtensionId, err
		}

		// First byte is empty
		payload := resHandshake.payload[1:]
		// Decode the bencoded map
		decoded, _, _ := decodeDictionary(string(payload))

		// The resulting map has a "m" key which contains the metadata
		var mMap map[string]any
		mMap = decoded["m"].(map[string]any)

		// Get the ID of the ut_metadata extension
		peerMetadataExtensionId = mMap["ut_metadata"].(int)
	}

	peerId = toHex(res[48:])
	return peerId, peerMetadataExtensionId, nil
}

func (t *torrent) magnetInfo() error {
	peers, err := t.peers()
	if err != nil {
		return err
	}

	peer := peers[0]

	conn, closer, err := newPeerConnection(peer)
	defer closer()

	// Traditional handshake
	handshakeResponse, err := t.handshake(conn, true)
	if err != nil {
		return err
	}

	// Receive bitfield
	_, err = conn.receivePeerMessage()
	if err != nil {
		return err
	}

	// Just as the handshake message sent, the received message has 8 reserved bytes
	// If the peer supports extensions, the 6 byte is set to 16
	peerSupportsExtensions := handshakeResponse[25] == 16
	if peerSupportsExtensions {
		// If the peer handles extensions, send extension handshake
		extensionHandshake := buildExtensionHandshakeMessage()
		_, err := conn.sendMessage(extensionHandshake)
		if err != nil {
			return err
		}

		// Receive extension handshake response
		extensionHandshakeResponse, err := conn.receivePeerMessage()
		if err != nil {
			return err
		}

		// Decode the bencoded map. Payload comes after first byte
		decoded, _, _ := decodeDictionary(string(extensionHandshakeResponse.payload[1:]))

		// The resulting map has a "m" key which contains the metadata
		var mMap map[string]any
		mMap = decoded["m"].(map[string]any)

		// Get the ID of the ut_metadata extension
		peerMetadataExtensionId := mMap["ut_metadata"].(int)

		metadataRequestMessage := buildMetadataRequestMessage(peerMetadataExtensionId)
		_, err = conn.sendMessage(metadataRequestMessage)
		if err != nil {
			return err
		}

		// Receive metadata 'data' message
		dataMessage, err := conn.receivePeerMessage()
		if err != nil {
			return err
		}

		_, usedBytes, err := decodeDictionary(string(dataMessage.payload[1:]))
		if err != nil {
			return err
		}

		metadata, _, err := decodeDictionary(string(dataMessage.payload[usedBytes+1:]))
		piecesStr := metadata["pieces"].(string)

		n := len(piecesStr) / 20
		pieces := make([][]byte, n)

		for i := 0; i < n; i++ {
			pieceStr := piecesStr[i*20 : (i+1)*20]
			pieces[i] = []byte(pieceStr)
		}

		t.info = info{
			length:      metadata["length"].(int),
			name:        metadata["name"].(string),
			nPieces:     n,
			pieceLength: metadata["piece length"].(int),
			pieces:      pieces,
		}
	}

	return nil
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
		_, err = conn.sendMessage(interestedMessage)

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

	//fmt.Printf("Piece will be divided in %d blocks\n", nBlocks+1)

	for i := 0; i < nBlocks; i++ {
		begin := i * blockSize
		blockLength := blockSize
		if i == nBlocks-1 {
			// All message requests will ask for exactly blockSize bytes, except the last one which most likely ask for
			// the remaining amount of bytes
			blockLength = pieceLength - begin
		}

		requestMessage := buildRequestMessage(pieceIndex, begin, blockLength)
		//fmt.Printf(" Requesting block %d with block length: %d\n", i, blockLength)
		_, err := conn.sendMessage(requestMessage)
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
		//fmt.Printf(" Received piece message for block %d\n", i)

		// Piece message payload is: 4 bytes for index. 4 bytes for begin. Rest of the bytes are the piece data
		// Ignore the first 8 bytes, and only use the actual piece data
		pieceData = append(pieceData, piece.payload[8:]...)
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
	_, err = t.handshake(conn, false)
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

	fileData := make([]byte, t.info.length)

	wg := sync.WaitGroup{}
	wg.Add(t.info.nPieces)

	for pieceIndex, pieceHash := range t.info.pieces {
		go func() {
			defer wg.Done()

			address := peers[mathRand.Intn(len(peers))]
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
				_, err = t.handshake(conn, false)
				if err != nil {
					fmt.Println(err)
				}
			}

			fmt.Printf("Downloading piece %d from peer %s\n", pieceIndex, address)

			// Get piece data
			// If connection already exists (we had downloaded a piece from that peer),
			// skip the initial messages: bitfield, interested, unchoke
			pieceData, err := t.getPieceFromPeer(conn, pieceIndex, !ok)
			if err != nil {
				fmt.Println(err)
				return
			}

			expectedHash := toHex(pieceHash)
			//fmt.Printf("Expected piece hash:    %s\n", expectedHash)

			h := sha1.New()
			h.Write(pieceData)
			writtenPieceHash := toHex(h.Sum(nil))
			//fmt.Printf("Downloaded piece hash:  %s\n", writtenPieceHash)

			if expectedHash != writtenPieceHash {
				fmt.Printf(" !! Piece hashes do not mash. Terminating")
				return
			}

			copy(fileData[pieceIndex*t.info.pieceLength:], pieceData)
			fmt.Printf(" Downloaded piece %d\n", pieceIndex)
			//fileData = append(fileData, pieceData...)
		}()
	}

	wg.Wait()

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
