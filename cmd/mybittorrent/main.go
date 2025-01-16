package main

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	// bencode "github.com/jackpal/bencode-go" // Available if you need it!
)

// Ensures gofmt doesn't remove the "os" encoding/json import (feel free to remove this!)
var _ = json.Marshal

func peers(metaInfo map[string]any) ([]string, error) {
	trackerUrl, ok := metaInfo["announce"].(string)
	if !ok {
		return nil, errors.New("in metainfo map 'announce' must be a string")
	}

	client := &http.Client{
		Timeout: time.Second * 10,
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, trackerUrl, nil)
	if err != nil {
		return nil, err
	}

	queryParams, err := peersQueryParams(metaInfo, req)
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

// peersQueryParams builds the query parameters needed to execute the peers request, using the metainfo map. Returns
// a string containing the URL encoded query parameters
func peersQueryParams(metaInfo map[string]any, req *http.Request) (string, error) {
	infoDict, ok := metaInfo["info"].(map[string]any)
	if !ok {
		return "", errors.New("info must be a map[string]any")
	}

	length, ok := infoDict["length"].(int)
	if !ok {
		return "", errors.New("length must be an int")
	}

	q := req.URL.Query()
	q.Add("info_hash", infoHash(infoDict))
	q.Add("peer_id", "kaykos-go-bittorrent")
	q.Add("port", "6881")
	q.Add("uploaded", "0")
	q.Add("downloaded", "0")
	q.Add("left", strconv.Itoa(length))
	q.Add("compact", "1")

	return q.Encode(), nil
}

func fileInfo(fileName string) (map[string]any, error) {
	file, err := os.Open(fileName)
	if err != nil {
		return nil, err
	}

	defer file.Close()

	fileContent, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	torrentDict, _, err := decodeDictionary(string(fileContent))
	if err != nil {
		return nil, err
	}

	return torrentDict, nil
}

func fileInfoStr(metaInfo map[string]any) (string, error) {
	announce := metaInfo["announce"]
	infoDict, ok := metaInfo["info"].(map[string]any)

	if !ok {
		return "", errors.New("info is not a map")
	}

	fileSize := infoDict["length"]
	hashInfo := toHex(infoHash(infoDict))
	pieceLength := infoDict["piece length"]

	pieces, ok := infoDict["pieces"].(string)
	hashPieces := pieceHashes(pieces)
	hashPiecesStr := strings.Join(hashPieces, "\n")

	return fmt.Sprintf("Tracker URL: %s\nLength: %d\nInfo Hash: %s\nPiece Length: %d\nPiece Hashes:\n%s", announce, fileSize, hashInfo, pieceLength, hashPiecesStr), nil
}

// infoHash bencodes the info map and returns the SHA-1 hash string representation
func infoHash(info map[string]any) string {
	infoStr := bencodeMap(info)

	h := sha1.New()
	h.Write([]byte(infoStr))

	return string(h.Sum(nil))
}

func toHex(s string) string {
	return hex.EncodeToString([]byte(s))
}

func pieceHashes(pieces string) []string {
	n := len(pieces) / 20
	hashes := make([]string, 0, n)

	for i := 0; i < n; i++ {
		hash := pieces[i*20 : i*20+20]
		hexHash := hex.EncodeToString([]byte(hash))
		hashes = append(hashes, hexHash)
	}

	return hashes
}

func main() {
	command := os.Args[1]
	//command = "info"

	if command == "decode" {
		bencodedValue := os.Args[2]
		//bencodedValue := "d3:foo3:bar5:helloi52ee"

		decoded, _, err := decodeValue(bencodedValue)
		if err != nil {
			fmt.Println(err)
			return
		}

		jsonOutput, _ := json.Marshal(decoded)
		fmt.Println(string(jsonOutput))
	} else if command == "info" {
		file := os.Args[2]
		//file = "sample.torrent"

		fileInfo, err := fileInfo(file)
		if err != nil {
			fmt.Println(err)
			return
		}

		infoStr, err := fileInfoStr(fileInfo)
		if err != nil {
			fmt.Println(err)
			return
		}

		fmt.Println(infoStr)
	} else if command == "peers" {
		file := os.Args[2]

		fileInfo, err := fileInfo(file)
		if err != nil {
			fmt.Println(err)
			return
		}

		peerAddresses, err := peers(fileInfo)
		if err != nil {
			fmt.Println(err)
			return
		}
		for _, peer := range peerAddresses {
			fmt.Println(peer)
		}

	} else {
		fmt.Println("Unknown command: " + command)
		os.Exit(1)
	}
}
