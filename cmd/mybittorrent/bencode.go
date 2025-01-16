package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// decodeValue decodes a bencoded string into a native Go type. Return value varies according the given string
func decodeValue(bencodedString string) (any, int, error) {
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

// decodeString decodes a bencoded string.
// Strings come as "10:strawberry", the initial number is the length of the encoded string
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

// decodeInteger decodes a bencoded integer.
// Integers come as "i52e"
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

// decodeList decodes a bencoded list string.
// Lists come in the format: "l<bencoded_elements>e"
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
		val, count, err := decodeValue(elementsStr)
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

// decodeDictionary decodes a bencoded integer string.
// Dictionaries come as "d<key1><value1>...<keyN><valueN>e"
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
		val, count, err := decodeValue(elementsStr)
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

// bencodeValue takes a parameter of any type and returns the bencoded string representation
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
