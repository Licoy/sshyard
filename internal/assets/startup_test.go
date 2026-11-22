package assets

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestStartupLines(t *testing.T) {
	got := StartupLines("export A=1  \r\n\r\n  \n  source ~/env\rcd /srv\n")
	if want := []string{"export A=1", "  source ~/env", "cd /srv"}; !slices.Equal(got, want) {
		t.Fatalf("lines %q, want %q", got, want)
	}
	if got := StartupLines(" \n\n"); got != nil {
		t.Fatalf("blank text %q", got)
	}
}

func TestValidateStartup(t *testing.T) {
	for _, tc := range []struct {
		text, reason string
	}{
		{"echo hi\nexport A=1", ""},
		{"", ""},
		{"echo\thi", "invalid"},
		{"echo \x1b[A", "invalid"},
		{"echo \x7f", "invalid"},
		{"\xff", "invalid"},
		{strings.Repeat("a", 4<<10+1), "tooLong"},
		{strings.Repeat("echo\n", 33), "tooLong"},
		{strings.Repeat("echo\n\n", 32), ""},
	} {
		err := ValidateStartup(tc.text)
		var fe *FieldError
		switch {
		case tc.reason == "" && err != nil:
			t.Errorf("%q: %v", tc.text, err)
		case tc.reason != "" && (!errors.As(err, &fe) || fe.Field != "startup" || fe.Reason != tc.reason):
			t.Errorf("%q: %v, want %s", tc.text, err, tc.reason)
		}
	}
}

func TestStartupOnlyOnSSH(t *testing.T) {
	h := Host{Name: "sw", Protocol: ProtoTelnet, Address: "10.0.0.1", Startup: true}
	h.Normalize()
	if h.Startup {
		t.Fatal("a Telnet host kept startup commands")
	}
	h = Host{Name: "web", Address: "10.0.0.1", User: "root", Startup: true}
	h.Normalize()
	if !h.Startup {
		t.Fatal("an SSH host lost its startup commands")
	}
}
