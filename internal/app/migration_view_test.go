package app

import (
	"fmt"
	"testing"

	"github.com/Licoy/sshyard/internal/migration"
	"github.com/Licoy/sshyard/locales"
)

func TestMigrationReadErrorMapping(t *testing.T) {
	for _, lang := range []string{"en", "zh-Hans", "zh-Hant", "ko", "ja"} {
		m := locales.In(lang)
		for _, row := range []struct {
			err  error
			want string
		}{{migration.ErrRead, m.MigrationReadError()}, {migration.ErrFormat, m.MigrationFormatError()}, {&migration.FormatError{File: "a.ini", Line: 3}, m.MigrationFormatErrorAt("3", "a.ini")}, {&migration.FormatError{File: "a.ini"}, m.MigrationFormatError()}, {migration.ErrNoEntries, m.MigrationEmpty()}, {migration.ErrTooLarge, m.MigrationTooLarge()}, {migration.ErrEncrypted, m.MigrationEncryptedSource()}} {
			if got := migrationReadError(m, fmt.Errorf("private diagnostic: %w", row.err)); got != row.want {
				t.Fatalf("%s: got %q want %q", lang, got, row.want)
			}
		}
	}
}
