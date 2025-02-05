package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
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

func (t torrent) magnetHandshake() (string, error) {
	peers, err := t.peers()
	if err != nil {
		return "", err
	}

	return t.peerHandshake(peers[0], true)
}
