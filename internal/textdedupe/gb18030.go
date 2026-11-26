package textdedupe

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// Downloaded Windows TXT files use the legacy GB18030-2000 mapping, including
// its private-use characters. The web-oriented x/text table omits or remaps
// some of these characters. Complete the two-byte table rather than replacing
// them with U+FFFD. Together with the four-byte ranges this mapping is bijective:
// valid input can round-trip without allocating a second whole-file encoding.
var gb18030TwoByte = func() [126 * 190]rune {
	var table [126 * 190]rune
	decoder := simplifiedchinese.GB18030.NewDecoder()
	var input [2]byte
	var output [4]byte
	for i := range table {
		input[0] = byte(i/190 + 0x81)
		input[1] = byte(i%190 + 0x40)
		if input[1] >= 0x7f {
			input[1]++
		}
		n, consumed, err := decoder.Transform(output[:], input[:], true)
		if err != nil || consumed != len(input) {
			panic("invalid GB18030 two-byte table")
		}
		table[i], _ = utf8.DecodeRune(output[:n])
	}
	// Entries are inclusive two-byte pointer ranges and their first scalar.
	// Pointers skip trail byte 0x7f. These are the legacy private-use mappings,
	// including A3 A0 (U+E5E5), which must not collapse into U+3000.
	for _, r := range []struct {
		first, last int
		scalar      rune
	}{
		{6080, 6175, 0xe4c6}, {6270, 6365, 0xe526},
		{6376, 6381, 0xe766}, {6433, 6433, 0xe76d},
		{6444, 6445, 0xe76e}, {6458, 6459, 0xe770},
		{6460, 6555, 0xe586}, {6650, 6745, 0xe5e6},
		{6829, 6839, 0xe772}, {6840, 6935, 0xe646},
		{7022, 7029, 0xe77d}, {7030, 7125, 0xe6a6},
		{7150, 7157, 0xe785}, {7182, 7188, 0xe78d},
		{7201, 7202, 0xe794}, {7208, 7208, 0xe796},
		{7211, 7219, 0xe797}, {7220, 7315, 0xe706},
		{7349, 7363, 0xe7a0}, {7397, 7409, 0xe7af},
		{7495, 7505, 0xe7bc}, {7533, 7533, 0xe7c7},
		{7538, 7541, 0xe7c9}, {7579, 7599, 0xe7cd},
		{7624, 7624, 0xe7e2}, {7627, 7627, 0xe7e3},
		{7629, 7631, 0xe7e4}, {7686, 7698, 0xe7f4},
		{7775, 7789, 0xe801}, {7886, 7979, 0xe000},
		{8076, 8169, 0xe05e}, {8266, 8359, 0xe0bc},
		{8456, 8549, 0xe11a}, {8646, 8739, 0xe178},
		{8836, 8929, 0xe1d6}, {16525, 16529, 0xe810},
		{22706, 22799, 0xe234}, {22896, 22989, 0xe292},
		{23086, 23179, 0xe2f0}, {23276, 23369, 0xe34e},
		{23466, 23559, 0xe3ac}, {23656, 23749, 0xe40a},
		{23767, 23769, 0xe816}, {23775, 23775, 0xe81e},
		{23783, 23783, 0xe826}, {23788, 23789, 0xe82b},
		{23794, 23795, 0xe831}, {23804, 23804, 0xe83b},
		{23812, 23812, 0xe843}, {23829, 23830, 0xe854},
		{23845, 23845, 0xe864}, {23846, 23939, 0xe468},
	} {
		for i := r.first; i <= r.last; i++ {
			table[i] = r.scalar + rune(i-r.first)
		}
	}
	return table
}()

func decodeGB18030(data []byte) (string, error) {
	var out strings.Builder
	out.Grow(len(data))
	decoder := simplifiedchinese.GB18030.NewDecoder()
	var fourByte [4]byte
	for i := 0; i < len(data); {
		first := data[i]
		if first < utf8.RuneSelf {
			out.WriteByte(first)
			i++
			continue
		}
		if first < 0x81 || first > 0xfe || i+1 >= len(data) {
			return "", fmt.Errorf("GB18030 在字节 %d 处包含无效或不完整的编码", i+1)
		}
		second := data[i+1]
		if second >= 0x30 && second <= 0x39 {
			if i+3 >= len(data) || data[i+2] < 0x81 || data[i+2] > 0xfe || data[i+3] < 0x30 || data[i+3] > 0x39 {
				return "", fmt.Errorf("GB18030 在字节 %d 处包含无效四字节编码", i+1)
			}
			pointer := ((int(first)-0x81)*10+int(second)-0x30)*1260 + (int(data[i+2])-0x81)*10 + int(data[i+3]) - 0x30
			if pointer >= 39420 && pointer < 189000 || pointer > 1237575 {
				return "", fmt.Errorf("GB18030 在字节 %d 处引用未分配码位", i+1)
			}
			n, consumed, err := decoder.Transform(fourByte[:], data[i:i+4], true)
			if err != nil || consumed != 4 {
				return "", fmt.Errorf("GB18030 在字节 %d 处无法完整解码", i+1)
			}
			out.Write(fourByte[:n])
			i += 4
			continue
		}
		if second < 0x40 || second == 0x7f || second > 0xfe {
			return "", fmt.Errorf("GB18030 在字节 %d 处包含无效双字节编码", i+1)
		}
		trail := int(second) - 0x40
		if second > 0x7f {
			trail--
		}
		out.WriteRune(gb18030TwoByte[(int(first)-0x81)*190+trail])
		i += 2
	}
	return out.String(), nil
}
