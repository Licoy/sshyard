//go:build !windows

package migration

// ansiCodePage is empty: this system has no ANSI code page.
func ansiCodePage() Encoding { return "" }
