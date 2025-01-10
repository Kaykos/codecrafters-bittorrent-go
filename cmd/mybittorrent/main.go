package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"
	// bencode "github.com/jackpal/bencode-go" // Available if you need it!
)

// Ensures gofmt doesn't remove the "os" encoding/json import (feel free to remove this!)
var _ = json.Marshal

// Example:
// - 5:hello -> hello
// - 10:hello12345 -> hello12345
func decodeBencode(bencodedString string) (interface{}, int, error) {
	if isBencodedString(bencodedString) {
		return decodeString(bencodedString)
	} else if isBencodedInteger(bencodedString) {
		return decodeInteger(bencodedString)
	} else if isBencodedList(bencodedString) {
		return decodeList(bencodedString)
	} else {
		return "", 0, fmt.Errorf("Only strings are supported at the moment")
	}
}

// Check if the first position of the string is a numerical digit
func isBencodedString(bencodedString string) bool {
	return unicode.IsDigit(rune(bencodedString[0]))
}

// Check if the first position of the string is an 'i'
func isBencodedInteger(bencodedString string) bool {
	return bencodedString[0] == 'i'
}

// Check if the first position of the string is an 'l'
func isBencodedList(bencodedString string) bool {
	return bencodedString[0] == 'l'
}

// Lists come as "l<bencoded_elements>e"
func decodeList(bencodedString string) (interface{}, int, error) {
	// Remove initial 'l'
	elementsStr := bencodedString[1:]
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

	return intVal, len(intStr) + 2, nil
}

func main() {
	command := os.Args[1]
	//command := "decode"

	if command == "decode" {
		bencodedValue := os.Args[2]
		//bencodedValue := "l10:strawberryi39ee"

		decoded, _, err := decodeBencode(bencodedValue)
		if err != nil {
			fmt.Println(err)
			return
		}

		jsonOutput, _ := json.Marshal(decoded)
		fmt.Println(string(jsonOutput))
	} else {
		fmt.Println("Unknown command: " + command)
		os.Exit(1)
	}
}
