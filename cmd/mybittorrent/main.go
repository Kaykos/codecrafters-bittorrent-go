package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	// bencode "github.com/jackpal/bencode-go" // Available if you need it!
)

// Ensures gofmt doesn't remove the "os" encoding/json import (feel free to remove this!)
var _ = json.Marshal

// Example:
// - 5:hello -> hello
// - 10:hello12345 -> hello12345
func decodeBencode(bencodedString string) (any, int, error) {
	switch bencodedString[0] {
	case 'i':
		return decodeInteger(bencodedString)
	case 'l':
		return decodeList(bencodedString)
	case 'd':
		return decodeDictionary(bencodedString)
	default:
		return decodeString(bencodedString)
	}
}

// Lists come as "l<bencoded_elements>e"
func decodeList(bencodedString string) ([]any, int, error) {
	// Remove initial 'l'
	elementsStr := bencodedString[1:] // e
	// Slice of decoded elements
	elements := []any{}
	// Processed bytes for the whole list string
	processed := 0
	for {
		// Found the end of the list
		if elementsStr[0] == 'e' {
			break
		}

		// Decode single element
		val, count, err := decodeBencode(elementsStr)
		if err != nil {
			return nil, 0, err
		}

		elements = append(elements, val)
		processed += count

		// Move the initial position by the amount of processed bytes of the element
		elementsStr = elementsStr[count:]
	}

	// +2 to account for the 'l' and 'e'
	return elements, processed + 2, nil
}

// Dictionries come as "d<key1><value1>...<keyN><valueN>e"
func decodeDictionary(bencodedString string) (map[string]any, int, error) {
	// Remove initial 'd'
	elementsStr := bencodedString[1:]
	// Map of decoded elements
	elements := map[string]any{}
	// Processed bytes for the whole dictionary string
	processed := 0
	for {
		// Found the end of the dictionary
		if elementsStr[0] == 'e' {
			break
		}

		// Decode single element
		key, count, err := decodeString(elementsStr)
		if err != nil {
			return nil, 0, err
		}

		// Move the initial position by the amount of processed bytes of the element
		elementsStr = elementsStr[count:]
		processed += count

		// Decode single element
		val, count, err := decodeBencode(elementsStr)
		if err != nil {
			return nil, 0, err
		}

		// Move the initial position by the amount of processed bytes of the element
		elementsStr = elementsStr[count:]
		processed += count

		elements[key] = val
	}

	// +2 to account for the 'd' and 'e'
	return elements, processed + 2, nil
}

// Strings come as "10:strawberry"
func decodeString(bencodedString string) (string, int, error) {
	firstColonIndex := strings.IndexByte(bencodedString, ':')

	// Length of the segment before the semicolon
	lengthStr := bencodedString[:firstColonIndex]

	// Actual length of the string to decode
	length, err := strconv.Atoi(lengthStr)
	if err != nil {
		return "", 0, err
	}

	return bencodedString[firstColonIndex+1 : firstColonIndex+1+length],
		length + len(lengthStr) + 1, // All the processed bytes, +1 to account for the ':'
		nil
}

// i52e
func decodeInteger(bencodedString string) (int, int, error) {
	firstEIndex := strings.IndexByte(bencodedString, 'e')

	if firstEIndex == 0 {
		return 0, 0, fmt.Errorf("Invalid encoded integer")
	}

	// Convert integer part of the string
	intStr := bencodedString[1:firstEIndex]
	intVal, err := strconv.Atoi(intStr)
	if err != nil {
		return 0, 0, err
	}

	// +2 to account for 'i' and 'e'
	return intVal, len(intStr) + 2, nil
}

func fileInfo(fileName string) (string, error) {
	file, err := os.Open(fileName)
	if err != nil {
		return "", err
	}

	defer file.Close()

	fileContent, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}

	torrentDict, _, err := decodeDictionary(string(fileContent))
	if err != nil {
		return "", err
	}

	announce := torrentDict["announce"]
	infoDict, ok := torrentDict["info"].(map[string]any)

	if !ok {
		return "", errors.New("info is not a map")
	}

	fileSize := infoDict["length"]
	hash := infoHash(infoDict)

	return fmt.Sprintf("Tracker URL: %s\nLength: %d\nInfo Hash: %s", announce, fileSize, hash), nil
}

// Func infoHash bencodes the info map and returns the SHA-1 hash represented in hexadecimal format
func infoHash(info map[string]any) string {
	infoStr := bencodeMap(info)

	h := sha1.New()
	h.Write([]byte(infoStr))

	return hex.EncodeToString(h.Sum(nil))
}

func bencodeValue(v any) string {
	var bencoded string

	switch v := v.(type) {
	case string:
		bencoded = bencodeString(v)
	case int:
		bencoded = bencodeInteger(v)
	case []any:
		bencoded = bencodeList(v)
	case map[string]any:
		bencoded = bencodeMap(v)
	}

	return bencoded
}

func bencodeString(s string) string {
	return fmt.Sprintf("%d:%s", len(s), s)
}

func bencodeInteger(i int) string {
	return fmt.Sprintf("i%de", i)
}

func bencodeList(l []any) string {
	var builder strings.Builder

	builder.WriteByte('l')
	for v := range l {
		builder.WriteString(bencodeValue(v))
	}
	builder.WriteByte('e')

	return builder.String()
}

func bencodeMap(m map[string]any) string {
	var builder strings.Builder
	builder.WriteByte('d')

	// A bencoded dictionary must have its keys in lexicographical order
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	// Iterate map using the sorted keys
	for _, k := range keys {
		builder.WriteString(bencodeString(k))
		builder.WriteString(bencodeValue(m[k]))
	}

	builder.WriteByte('e')

	return builder.String()
}

func main() {
	command := os.Args[1]
	//command = "info"

	if command == "decode" {
		bencodedValue := os.Args[2]
		//bencodedValue := "d3:foo3:bar5:helloi52ee"

		decoded, _, err := decodeBencode(bencodedValue)
		if err != nil {
			fmt.Println(err)
			return
		}

		jsonOutput, _ := json.Marshal(decoded)
		fmt.Println(string(jsonOutput))
	} else if command == "info" {
		file := os.Args[2]
		//file = "sample.torrent"

		info, err := fileInfo(file)
		if err != nil {
			fmt.Println(err)
			return
		}

		fmt.Println(info)
	} else {
		fmt.Println("Unknown command: " + command)
		os.Exit(1)
	}
}
