package migration

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

// Encoding is a legacy code page an export that is neither UTF-8 nor
// UTF-16 with a byte order mark may be in: Windows clients write their
// INI files in the system's ANSI code page.
type Encoding string

const (
	GB18030     Encoding = "GB18030"
	Big5        Encoding = "Big5"
	ShiftJIS    Encoding = "Shift_JIS"
	EUCKR       Encoding = "EUC-KR"
	Windows1252 Encoding = "Windows-1252"
)

// Encodings are the legacy code pages, in the order detection tries
// them after the preferred ones; Windows-1252 decodes nearly any bytes,
// so it comes last.
func Encodings() []Encoding { return []Encoding{GB18030, Big5, ShiftJIS, EUCKR, Windows1252} }

// LanguageEncoding is the code page Windows writes legacy files in for a
// user of the language (a BCP 47 tag such as zh-Hans or ja).
func LanguageEncoding(locale string) Encoding {
	tag := strings.ToLower(locale)
	switch {
	case strings.HasPrefix(tag, "zh-hant"), strings.HasPrefix(tag, "zh-tw"), strings.HasPrefix(tag, "zh-hk"), strings.HasPrefix(tag, "zh-mo"):
		return Big5
	case tag == "zh" || strings.HasPrefix(tag, "zh-") || strings.HasPrefix(tag, "zh_"):
		return GB18030
	case tag == "ja" || strings.HasPrefix(tag, "ja-") || strings.HasPrefix(tag, "ja_"):
		return ShiftJIS
	case tag == "ko" || strings.HasPrefix(tag, "ko-") || strings.HasPrefix(tag, "ko_"):
		return EUCKR
	}
	return Windows1252
}

// Options says how Read decodes an export of an INI-style source that is
// neither UTF-8 nor UTF-16 with a byte order mark.
type Options struct {
	// Encoding is the code page such files are in; empty detects it.
	Encoding Encoding
	// Prefer is tried first when detecting, after the system's code page.
	Prefer Encoding
}

func (e Encoding) decoder() *encoding.Decoder {
	switch e {
	case GB18030:
		return simplifiedchinese.GB18030.NewDecoder()
	case Big5:
		return traditionalchinese.Big5.NewDecoder()
	case ShiftJIS:
		return japanese.ShiftJIS.NewDecoder()
	case EUCKR:
		return korean.EUCKR.NewDecoder()
	case Windows1252:
		return charmap.Windows1252.NewDecoder()
	}
	return nil
}

// systemEncoding is the system's ANSI code page, empty where there is none.
var systemEncoding = ansiCodePage

// legacy reports data that is neither UTF-8 nor UTF-16 with a byte order mark.
func legacy(data []byte) bool {
	return !utf8.Valid(data) && !bytes.HasPrefix(data, []byte{0xff, 0xfe}) && !bytes.HasPrefix(data, []byte{0xfe, 0xff})
}

// legacyText decodes data in the code page o names, else in the first
// one that decodes it cleanly: the system's, o.Prefer, then Encodings.
func legacyText(data []byte, o Options) ([]byte, Encoding, error) {
	candidates := []Encoding{o.Encoding}
	if o.Encoding == "" {
		candidates = append([]Encoding{systemEncoding(), o.Prefer}, Encodings()...)
	}
	for _, e := range candidates {
		if out, ok := decodeClean(data, e); ok {
			return out, e, nil
		}
	}
	return nil, "", ErrFormat
}

// decodeClean decodes data in e, refusing invalid sequences and what a
// wrong guess decodes to: C1 controls and private-use characters.
func decodeClean(data []byte, e Encoding) ([]byte, bool) {
	d := e.decoder()
	if d == nil {
		return nil, false
	}
	out, err := d.Bytes(data)
	if err != nil {
		return nil, false
	}
	for _, r := range string(out) {
		if r == utf8.RuneError || r == 0 || r >= 0x80 && r <= 0x9f || r >= 0xe000 && r <= 0xf8ff {
			return nil, false
		}
	}
	return out, true
}
