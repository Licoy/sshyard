package migration

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

// writeExport writes text in enc to a file of dir and returns its path.
func writeExport(t *testing.T, name, text string, enc encoding.Encoding) string {
	t.Helper()
	data := []byte(text)
	if enc != nil {
		var err error
		if data, err = enc.NewEncoder().Bytes(data); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// noSystemEncoding detects as on a system without an ANSI code page.
func noSystemEncoding(t *testing.T) {
	old := systemEncoding
	systemEncoding = func() Encoding { return "" }
	t.Cleanup(func() { systemEncoding = old })
}

func TestMobaXtermLegacyEncoding(t *testing.T) {
	noSystemEncoding(t)
	for _, tc := range []struct {
		enc          encoding.Encoding
		want         Encoding
		group, label string
	}{
		{simplifiedchinese.GBK, GB18030, "数据库", "生产服务器"},
		{traditionalchinese.Big5, Big5, "資料庫", "正式伺服器"},
		{japanese.ShiftJIS, ShiftJIS, "データベース", "本番サーバー"},
		{korean.EUCKR, EUCKR, "데이터베이스", "운영서버"},
		{charmap.Windows1252, Windows1252, "Müller", "café"},
	} {
		t.Run(string(tc.want), func(t *testing.T) {
			path := writeExport(t, "sessions.mxtsessions", "[Bookmarks]\nSubRep="+tc.group+"\n"+tc.label+"=#109#0%example.org%22%alice#font", tc.enc)
			r, err := Read(MobaXterm, []string{path}, Options{Prefer: tc.want})
			if err != nil || len(r.Entries) != 1 {
				t.Fatalf("entries=%d error=%v", len(r.Entries), err)
			}
			if e := r.Entries[0]; e.Group != tc.group || e.Name != tc.label || r.Encoding != tc.want {
				t.Fatalf("group=%q name=%q encoding=%q", e.Group, e.Name, r.Encoding)
			}
		})
	}
}

func TestLegacyEncodingChoice(t *testing.T) {
	noSystemEncoding(t)
	gbk := writeExport(t, "sessions.mxtsessions", "[Bookmarks]\nSubRep=数据库\nnode=#109#0%example.org%22%alice#font", simplifiedchinese.GBK)
	// Without a preference, the CJK code pages are tried before Windows-1252.
	r, err := Read(MobaXterm, []string{gbk}, Options{})
	if err != nil || r.Entries[0].Group != "数据库" || r.Encoding != GB18030 {
		t.Fatalf("detection: %#v %v", r, err)
	}
	// The code page chosen is used even where another one was detected.
	r, err = Read(MobaXterm, []string{gbk}, Options{Encoding: Windows1252, Prefer: GB18030})
	if err != nil || r.Entries[0].Group != "Êý¾Ý¿â" || r.Encoding != Windows1252 {
		t.Fatalf("choice: %#v %v", r, err)
	}
	// The system's code page comes before the preferred one.
	systemEncoding = func() Encoding { return Windows1252 }
	if r, err = Read(MobaXterm, []string{gbk}, Options{Prefer: GB18030}); err != nil || r.Encoding != Windows1252 {
		t.Fatalf("system: %#v %v", r, err)
	}
	// A code page that cannot decode the file fails it, naming the file.
	latin := writeExport(t, "latin.ini", "[Bookmarks]\nnode=#109#0%caf\xe9 %22%alice#font", nil)
	var fe *FormatError
	if _, err = Read(MobaXterm, []string{latin}, Options{Encoding: GB18030}); !errors.As(err, &fe) || fe.File != "latin.ini" || !errors.Is(err, ErrFormat) {
		t.Fatalf("undecodable: %v", err)
	}
	// UTF-8 is not a legacy code page.
	utf := writeExport(t, "utf8.ini", "[Bookmarks]\nSubRep=数据库\nnode=#109#0%example.org%22%alice#font", nil)
	if r, err = Read(MobaXterm, []string{utf}, Options{Encoding: Windows1252}); err != nil || r.Entries[0].Group != "数据库" || r.Encoding != "" {
		t.Fatalf("utf-8: %#v %v", r, err)
	}
}

func TestLanguageEncoding(t *testing.T) {
	for tag, want := range map[string]Encoding{"zh-Hans": GB18030, "zh": GB18030, "zh-CN": GB18030, "zh-Hant": Big5, "zh-TW": Big5, "ja": ShiftJIS, "ko": EUCKR, "en": Windows1252, "": Windows1252} {
		if got := LanguageEncoding(tag); got != want {
			t.Errorf("%q: got %q want %q", tag, got, want)
		}
	}
}

func TestMobaXtermDamagedBookmarks(t *testing.T) {
	data := "[Bookmarks]\nSubRep=team\nshort=#109#0%example.org\ngood=#109#0%example.org%22%alice#font\nicon=#x#0%example.org%22%alice#font\nbare=nothing\nrdp=#91#4%example.org%3389%alice#font"
	r, err := parseMobaXterm([]byte(data), "sessions")
	if err != nil || len(r.Entries) != 1 || r.Entries[0].Name != "good" {
		t.Fatalf("good bookmark lost: %#v %v", r, err)
	}
	want := []Issue{{Code: "damaged-entry", Name: "short", Line: 3}, {Code: "damaged-entry", Name: "icon", Line: 5}, {Code: "damaged-entry", Name: "bare", Line: 6}, {Code: "unsupported-protocol", Name: "rdp", Field: "SessionType", Line: 7}}
	if len(r.Issues) != len(want) {
		t.Fatalf("issues %#v", r.Issues)
	}
	for i := range want {
		if r.Issues[i] != want[i] {
			t.Fatalf("issue %d: got %#v want %#v", i, r.Issues[i], want[i])
		}
	}
}

func TestMobaXtermDefaultUser(t *testing.T) {
	bookmark := "[Bookmarks]\nnode=#109#0%example.org%22%<default>#font\n"
	r, err := parseMobaXterm([]byte("[Misc]\nDefaultLoginName=ops\n"+bookmark), "MobaXterm.ini")
	if err != nil || r.Entries[0].User != "ops" || len(r.Entries[0].Issues) != 0 {
		t.Fatalf("default user not used: %#v %v", r, err)
	}
	r, err = parseMobaXterm([]byte(bookmark+"[Misc]\nDefaultLoginName=ops\n"), "MobaXterm.ini")
	if err != nil || r.Entries[0].User != "ops" {
		t.Fatalf("default user after the bookmarks not used: %#v %v", r, err)
	}
	r, err = parseMobaXterm([]byte(bookmark), "sessions.mxtsessions")
	if err != nil || r.Entries[0].User != "" || len(r.Entries[0].Issues) != 1 || r.Entries[0].Issues[0].Code != "default-user" {
		t.Fatalf("missing default user not reported: %#v %v", r, err)
	}
}

func TestINISkipsMalformedLines(t *testing.T) {
	sections, err := iniRead([]byte("[ok]\na=b\ngarbage\n[bad\n[prod] web=x\n[next]"))
	if err != nil || len(sections) != 3 || sections[2].name != "next" {
		t.Fatalf("sections %#v %v", sections, err)
	}
	if ok := sections[1]; len(ok.pairs) != 2 || ok.pairs[1].key != "[prod] web" || len(ok.skipped) != 2 || ok.skipped[0] != 3 || ok.skipped[1] != 4 {
		t.Fatalf("section %#v", ok)
	}
}

func TestMobaXtermTolerantFile(t *testing.T) {
	data := "[Misc]\nNote=bad\x00\uFFFD\uFFFD\n[Bookmarks]\nSubRep=team\ngood=#109#0%example.org%22%alice#font\ngarbage\n[prod] web=#109#0%  web.example.org \t%0%bob#font\n"
	r, err := Read(MobaXterm, []string{writeExport(t, "MobaXterm.ini", data, nil)}, Options{})
	if err != nil || len(r.Entries) != 2 {
		t.Fatalf("entries %#v %v", r, err)
	}
	if e := r.Entries[1]; e.Name != "[prod] web" || e.Address != "web.example.org" || e.Port != 22 || e.User != "bob" || e.Group != "team" || len(e.Issues) != 0 {
		t.Fatalf("entry %#v", e)
	}
	if len(r.Issues) != 1 || r.Issues[0] != (Issue{Code: "damaged-entry", Line: 6}) {
		t.Fatalf("issues %#v", r.Issues)
	}
	// Sessions in a section other than Bookmarks are found by SubRep.
	r, err = parseMobaXterm([]byte("[Sessions]\nSubRep=\nnode=#109#0%example.org%22%alice#font"), "sessions")
	if err != nil || len(r.Entries) != 1 {
		t.Fatalf("SubRep section: %#v %v", r, err)
	}
	if _, err = parseMobaXterm([]byte("[Misc]\nDefaultLoginName=ops\n"), "MobaXterm.ini"); !errors.Is(err, ErrNoSessions) || !errors.Is(err, ErrFormat) {
		t.Fatalf("no sessions: %v", err)
	}
}
