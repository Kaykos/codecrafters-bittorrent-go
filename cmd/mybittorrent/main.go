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

func isBencodedString(bencodedString string) bool {
	return unicode.IsDigit(rune(bencodedString[0]))
}

func isBencodedInteger(bencodedString string) bool {
	return bencodedString[0] == 'i'
}

func isBencodedList(bencodedString string) bool {
	return bencodedString[0] == 'l'
}

func decodeList(bencodedString string) (interface{}, int, error) {
	//l5:helloi52ee
	//5:helloi52e
	elementsStr := bencodedString[1 : len(bencodedString)-1]
	elements := []any{}
	for len(elementsStr) > 0 {
		val, count, err := decodeBencode(elementsStr)
		if err != nil {
			return nil, 0, err
		}
		elements = append(elements, val)
		elementsStr = elementsStr[count:]
	}

	return elements, len(bencodedString), nil
}

func decodeString(bencodedString string) (string, int, error) {
	firstColonIndex := strings.IndexByte(bencodedString, ':')

	lengthStr := bencodedString[:firstColonIndex]

	length, err := strconv.Atoi(lengthStr)
	if err != nil {
		return "", 0, err
	}

	return bencodedString[firstColonIndex+1 : firstColonIndex+1+length], length + 2, nil
}

func decodeInteger(bencodedString string) (int, int, error) {
	eIndex := strings.IndexByte(bencodedString, 'e')

	if eIndex == 0 {
		return 0, 0, fmt.Errorf("Invalid encoded integer")
	}

	// Convert integer part of the string
	intStr := bencodedString[1:eIndex]
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
		//bencodedValue := "l5:helloi52ee"
		//bencodedValue := "5:hello"
		//bencodedValue := "i-123e"

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
