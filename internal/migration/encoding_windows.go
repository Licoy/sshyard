package migration

import "golang.org/x/sys/windows"

// ansiCodePage is the ANSI code page of this Windows, which the local
// clients write their INI files in.
func ansiCodePage() Encoding {
	switch windows.GetACP() {
	case 936, 54936:
		return GB18030
	case 950:
		return Big5
	case 932:
		return ShiftJIS
	case 949:
		return EUCKR
	case 1252:
		return Windows1252
	}
	return ""
}
