package main

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	// bencode "github.com/jackpal/bencode-go" // Available if you need it!
)

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
	left := t.info.length
	if left == 0 {
		// When downloading from magnet link, we don't know the file size. Hardcode a value
		left = 999
	}

	q := req.URL.Query()
	q.Add("info_hash", string(t.infoHash))
	q.Add("peer_id", "kaykos-go-bittorrent")
	q.Add("port", "6881")
	q.Add("uploaded", "0")
	q.Add("downloaded", "0")
	q.Add("left", strconv.Itoa(left))
	q.Add("compact", "1")

	return q.Encode(), nil
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

		peerId, err := torrent.peerHandshake(peerAddress, false)
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
		torrent, err := parseMagnetLink(magnetLink)
		if err != nil {
			fmt.Println(err)
			return
		}

		fmt.Printf("Tracker URL: %s\nInfo Hash: %s\n", torrent.announce, toHex(torrent.infoHash))
	} else if command == "magnet_handshake" {
		magnetLink := os.Args[2]
		torrent, err := parseMagnetLink(magnetLink)
		if err != nil {
			fmt.Println(err)
			return
		}

		peerId, peerExtensionId, err := torrent.magnetHandshake()
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Printf("Peer ID: %s\n", peerId)
		if peerExtensionId != 0 {
			fmt.Printf("Peer Metadata Extension ID: %d\n", peerExtensionId)

		}
	} else if command == "magnet_info" {
		magnetLink := os.Args[2]
		torrent, err := parseMagnetLink(magnetLink)
		if err != nil {
			fmt.Println(err)
			return
		}

		err = torrent.magnetInfo()
		if err != nil {
			fmt.Println(err)
			return
		}

		fmt.Println(torrent.infoStr())
	} else if command == "magnet_download_piece" {
		flag := os.Args[2]
		if flag != "-o" {
			fmt.Println("Missing output flag: '-o'")
			return
		}

		output := os.Args[3]
		magnetLink := os.Args[4]
		pieceIndex, err := strconv.Atoi(os.Args[5])
		if err != nil {
			fmt.Println(err)
			return
		}

		torrent, err := parseMagnetLink(magnetLink)
		if err != nil {
			fmt.Println(err)
			return
		}
		err = torrent.magnetInfo()
		if err != nil {
			fmt.Println(err)
			return
		}

		torrent.downloadPieceToFile(output, pieceIndex)
	} else if command == "magnet_download" {
		flag := os.Args[2]
		if flag != "-o" {
			fmt.Println("Missing output flag: '-o'")
			return
		}

		output := os.Args[3]
		magnetLink := os.Args[4]

		torrent, err := parseMagnetLink(magnetLink)
		if err != nil {
			fmt.Println(err)
			return
		}
		err = torrent.magnetInfo()
		if err != nil {
			fmt.Println(err)
			return
		}

		torrent.downloadFile(output)
	} else {
		fmt.Println("Unknown command: " + command)
		os.Exit(1)
	}
}
