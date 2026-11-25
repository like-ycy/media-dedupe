package textdedupe

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func readText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text, err := decodeText(data)
	if err != nil {
		return "", err
	}
	return normalize(text), nil
}

func decodeText(data []byte) (string, error) {
	if len(data) >= 2 && (bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff})) {
		var order binary.ByteOrder = binary.LittleEndian
		if data[0] == 0xfe {
			order = binary.BigEndian
		}
		data = data[2:]
		if len(data)%2 != 0 {
			return "", fmt.Errorf("UTF-16 字节数不完整")
		}
		units := make([]uint16, len(data)/2)
		for i := range units {
			units[i] = order.Uint16(data[i*2:])
		}
		for i := 0; i < len(units); i++ {
			u := units[i]
			if u >= 0xd800 && u <= 0xdbff {
				if i+1 >= len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
					return "", fmt.Errorf("UTF-16 代理对无效")
				}
				i++
			} else if u >= 0xdc00 && u <= 0xdfff {
				return "", fmt.Errorf("UTF-16 代理对无效")
			}
		}
		return string(utf16.Decode(units)), nil
	}
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		data = data[3:]
		if !utf8.Valid(data) {
			return "", fmt.Errorf("UTF-8 BOM 文件包含无效字节")
		}
	}
	if utf8.Valid(data) {
		return string(data), nil
	}
	decoded, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), data)
	if err != nil {
		return "", fmt.Errorf("GB18030 解码失败: %w", err)
	}
	// The legacy decoder can silently replace invalid input. A round trip must
	// preserve every source byte before decoded text can establish equality.
	encoded, _, err := transform.Bytes(simplifiedchinese.GB18030.NewEncoder(), decoded)
	if err != nil || !bytes.Equal(encoded, data) {
		return "", fmt.Errorf("GB18030 解码有损或包含无效字节")
	}
	return string(decoded), nil
}

// normalize deliberately changes line endings only. Case, spacing, Unicode
// compatibility characters, punctuation and advertisements remain significant.
func normalize(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
}

func textDigest(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
}
