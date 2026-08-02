// Package plist reads Apple property lists, binary and XML, without shelling
// out. A subprocess that fails prints an error to stdout, and an error string
// parsed as a value is how a cleanup tool ends up treating "File Doesn't Exist"
// as a path. Parsing in process makes a failure an error instead of a value.
package plist

import (
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// appleEpoch is 2001-01-01 UTC, which is what a binary plist date counts from.
var appleEpoch = time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC)

const maxFileBytes = 32 << 20

var (
	ErrUnsupported = errors.New("not a property list")
	ErrMalformed   = errors.New("malformed property list")
)

// Dict is the shape almost every caller wants.
type Dict map[string]any

func (d Dict) String(key string) (string, bool) {
	value, ok := d[key].(string)
	return value, ok
}

func (d Dict) Int(key string) (int64, bool) {
	switch value := d[key].(type) {
	case int64:
		return value, true
	case float64:
		return int64(value), true
	}
	return 0, false
}

func (d Dict) Bool(key string) (bool, bool) {
	value, ok := d[key].(bool)
	return value, ok
}

func (d Dict) Strings(key string) []string {
	values, ok := d[key].([]any)
	if !ok {
		if single, isString := d[key].(string); isString {
			return []string{single}
		}
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, isString := value.(string); isString {
			result = append(result, text)
		}
	}
	return result
}

func (d Dict) Dict(key string) (Dict, bool) {
	value, ok := d[key].(Dict)
	return value, ok
}

// ReadFile parses a plist from disk. It refuses anything implausibly large
// rather than reading a file that is not a plist into memory.
func ReadFile(path string) (Dict, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxFileBytes {
		return nil, fmt.Errorf("%w: %s is %d bytes", ErrUnsupported, path, info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

func Parse(data []byte) (Dict, error) {
	value, err := ParseAny(data)
	if err != nil {
		return nil, err
	}
	dict, ok := value.(Dict)
	if !ok {
		return nil, fmt.Errorf("%w: the root is not a dictionary", ErrUnsupported)
	}
	return dict, nil
}

func ParseAny(data []byte) (any, error) {
	trimmed := strings.TrimLeft(string(firstBytes(data, 64)), " \t\r\n")
	switch {
	case strings.HasPrefix(string(firstBytes(data, 8)), "bplist0"):
		return parseBinary(data)
	case strings.HasPrefix(trimmed, "<?xml"), strings.HasPrefix(trimmed, "<!DOCTYPE plist"), strings.HasPrefix(trimmed, "<plist"):
		return parseXML(data)
	default:
		return nil, ErrUnsupported
	}
}

func firstBytes(data []byte, count int) []byte {
	if len(data) < count {
		return data
	}
	return data[:count]
}

// binary format

type binaryReader struct {
	data        []byte
	offsets     []uint64
	refSize     int
	objectCount uint64
	// visiting guards against a container that reaches itself, which a
	// corrupt file can express and a recursive decoder would follow forever.
	visiting map[uint64]bool
}

func parseBinary(data []byte) (any, error) {
	if len(data) < 40 {
		return nil, fmt.Errorf("%w: too short for a trailer", ErrMalformed)
	}
	trailer := data[len(data)-32:]
	offsetSize := int(trailer[6])
	refSize := int(trailer[7])
	objectCount := binary.BigEndian.Uint64(trailer[8:16])
	topObject := binary.BigEndian.Uint64(trailer[16:24])
	tableOffset := binary.BigEndian.Uint64(trailer[24:32])

	if offsetSize < 1 || offsetSize > 8 || refSize < 1 || refSize > 8 {
		return nil, fmt.Errorf("%w: implausible integer widths", ErrMalformed)
	}
	if objectCount == 0 || objectCount > uint64(len(data)) {
		return nil, fmt.Errorf("%w: implausible object count", ErrMalformed)
	}
	tableEnd := tableOffset + objectCount*uint64(offsetSize)
	if tableOffset < 8 || tableEnd > uint64(len(data)) {
		return nil, fmt.Errorf("%w: the offset table does not fit", ErrMalformed)
	}

	offsets := make([]uint64, objectCount)
	for index := range offsets {
		start := tableOffset + uint64(index*offsetSize)
		offsets[index] = readBigEndian(data[start : start+uint64(offsetSize)])
	}
	reader := &binaryReader{
		data:        data,
		offsets:     offsets,
		refSize:     refSize,
		objectCount: objectCount,
		visiting:    make(map[uint64]bool),
	}
	return reader.object(topObject)
}

func readBigEndian(raw []byte) uint64 {
	var value uint64
	for _, singleByte := range raw {
		value = value<<8 | uint64(singleByte)
	}
	return value
}

func (r *binaryReader) object(index uint64) (any, error) {
	if index >= r.objectCount {
		return nil, fmt.Errorf("%w: object %d is out of range", ErrMalformed, index)
	}
	if r.visiting[index] {
		return nil, fmt.Errorf("%w: object %d contains itself", ErrMalformed, index)
	}
	offset := r.offsets[index]
	if offset >= uint64(len(r.data)) {
		return nil, fmt.Errorf("%w: object %d points past the end", ErrMalformed, index)
	}
	r.visiting[index] = true
	defer delete(r.visiting, index)

	marker := r.data[offset]
	kind := marker >> 4
	info := int(marker & 0x0F)
	body := offset + 1

	switch kind {
	case 0x0:
		switch info {
		case 0x0:
			return nil, nil
		case 0x8:
			return false, nil
		case 0x9:
			return true, nil
		default:
			return nil, fmt.Errorf("%w: unknown singleton 0x%02x", ErrMalformed, marker)
		}
	case 0x1:
		return r.integer(body, 1<<info)
	case 0x2:
		return r.real(body, 1<<info)
	case 0x3:
		seconds, err := r.real(body, 8)
		if err != nil {
			return nil, err
		}
		return appleEpoch.Add(time.Duration(seconds * float64(time.Second))), nil
	case 0x4:
		count, start, err := r.count(info, body)
		if err != nil {
			return nil, err
		}
		return r.slice(start, count)
	case 0x5:
		count, start, err := r.count(info, body)
		if err != nil {
			return nil, err
		}
		raw, err := r.slice(start, count)
		if err != nil {
			return nil, err
		}
		return string(raw), nil
	case 0x6:
		count, start, err := r.count(info, body)
		if err != nil {
			return nil, err
		}
		raw, err := r.slice(start, count*2)
		if err != nil {
			return nil, err
		}
		return decodeUTF16(raw), nil
	case 0x8:
		return r.integer(body, info+1)
	case 0xA, 0xC:
		count, start, err := r.count(info, body)
		if err != nil {
			return nil, err
		}
		return r.array(start, count)
	case 0xD:
		count, start, err := r.count(info, body)
		if err != nil {
			return nil, err
		}
		return r.dict(start, count)
	default:
		return nil, fmt.Errorf("%w: unknown marker 0x%02x", ErrMalformed, marker)
	}
}

// count reads the element count, which is either packed into the marker's low
// nibble or, when that nibble is full, held in an integer object that follows.
func (r *binaryReader) count(info int, body uint64) (uint64, uint64, error) {
	if info != 0x0F {
		return uint64(info), body, nil
	}
	if body >= uint64(len(r.data)) {
		return 0, 0, fmt.Errorf("%w: truncated count", ErrMalformed)
	}
	marker := r.data[body]
	if marker>>4 != 0x1 {
		return 0, 0, fmt.Errorf("%w: count is not an integer", ErrMalformed)
	}
	width := 1 << (marker & 0x0F)
	value, err := r.integer(body+1, width)
	if err != nil {
		return 0, 0, err
	}
	if value < 0 {
		return 0, 0, fmt.Errorf("%w: negative count", ErrMalformed)
	}
	return uint64(value), body + 1 + uint64(width), nil
}

func (r *binaryReader) integer(offset uint64, width int) (int64, error) {
	raw, err := r.slice(offset, uint64(width))
	if err != nil {
		return 0, err
	}
	// A binary plist stores 16-byte integers for values that do not fit in 8,
	// and only the low 8 bytes are meaningful for anything we read.
	if width > 8 {
		raw = raw[width-8:]
	}
	value := readBigEndian(raw)
	if width == 8 {
		return int64(value), nil
	}
	return int64(value), nil
}

func (r *binaryReader) real(offset uint64, width int) (float64, error) {
	raw, err := r.slice(offset, uint64(width))
	if err != nil {
		return 0, err
	}
	switch width {
	case 4:
		return float64(math.Float32frombits(uint32(readBigEndian(raw)))), nil
	case 8:
		return math.Float64frombits(readBigEndian(raw)), nil
	default:
		return 0, fmt.Errorf("%w: real of width %d", ErrMalformed, width)
	}
}

func (r *binaryReader) slice(offset, length uint64) ([]byte, error) {
	end := offset + length
	if end < offset || end > uint64(len(r.data)) {
		return nil, fmt.Errorf("%w: value runs past the end", ErrMalformed)
	}
	return r.data[offset:end], nil
}

func (r *binaryReader) references(offset, count uint64) ([]uint64, error) {
	raw, err := r.slice(offset, count*uint64(r.refSize))
	if err != nil {
		return nil, err
	}
	references := make([]uint64, count)
	for index := range references {
		start := index * r.refSize
		references[index] = readBigEndian(raw[start : start+r.refSize])
	}
	return references, nil
}

func (r *binaryReader) array(offset, count uint64) ([]any, error) {
	references, err := r.references(offset, count)
	if err != nil {
		return nil, err
	}
	values := make([]any, 0, count)
	for _, reference := range references {
		value, err := r.object(reference)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (r *binaryReader) dict(offset, count uint64) (Dict, error) {
	keys, err := r.references(offset, count)
	if err != nil {
		return nil, err
	}
	values, err := r.references(offset+count*uint64(r.refSize), count)
	if err != nil {
		return nil, err
	}
	dict := make(Dict, count)
	for index := range keys {
		key, err := r.object(keys[index])
		if err != nil {
			return nil, err
		}
		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("%w: a dictionary key is not a string", ErrMalformed)
		}
		value, err := r.object(values[index])
		if err != nil {
			return nil, err
		}
		dict[name] = value
	}
	return dict, nil
}

func decodeUTF16(raw []byte) string {
	units := make([]uint16, 0, len(raw)/2)
	for index := 0; index+1 < len(raw); index += 2 {
		units = append(units, binary.BigEndian.Uint16(raw[index:index+2]))
	}
	return string(utf16.Decode(units))
}

// xml format

func parseXML(data []byte) (any, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	decoder.Strict = false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: no plist element", ErrUnsupported)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local == "plist" {
			return xmlNextValue(decoder)
		}
	}
}

func xmlNextValue(decoder *xml.Decoder) (any, error) {
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			return xmlValue(decoder, element)
		case xml.EndElement:
			return nil, nil
		}
	}
}

func xmlValue(decoder *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		return xmlDict(decoder)
	case "array":
		return xmlArray(decoder)
	case "true":
		return true, decoder.Skip()
	case "false":
		return false, decoder.Skip()
	case "string", "key":
		return xmlText(decoder)
	case "integer":
		text, err := xmlText(decoder)
		if err != nil {
			return nil, err
		}
		return strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	case "real":
		text, err := xmlText(decoder)
		if err != nil {
			return nil, err
		}
		return strconv.ParseFloat(strings.TrimSpace(text), 64)
	case "date":
		text, err := xmlText(decoder)
		if err != nil {
			return nil, err
		}
		return time.Parse(time.RFC3339, strings.TrimSpace(text))
	case "data":
		return xmlText(decoder)
	default:
		return nil, decoder.Skip()
	}
}

func xmlText(decoder *xml.Decoder) (string, error) {
	var builder strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrMalformed, err)
		}
		switch element := token.(type) {
		case xml.CharData:
			builder.Write(element)
		case xml.EndElement:
			return builder.String(), nil
		}
	}
}

func xmlDict(decoder *xml.Decoder) (Dict, error) {
	dict := Dict{}
	var pending string
	haveKey := false
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			if element.Name.Local == "key" && !haveKey {
				pending, err = xmlText(decoder)
				if err != nil {
					return nil, err
				}
				haveKey = true
				continue
			}
			value, err := xmlValue(decoder, element)
			if err != nil {
				return nil, err
			}
			if haveKey {
				dict[pending] = value
				haveKey = false
			}
		case xml.EndElement:
			return dict, nil
		}
	}
}

func xmlArray(decoder *xml.Decoder) ([]any, error) {
	var values []any
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			value, err := xmlValue(decoder, element)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		case xml.EndElement:
			return values, nil
		}
	}
}
