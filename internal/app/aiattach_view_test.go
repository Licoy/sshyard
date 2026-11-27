package app

import (
	"fmt"
	"testing"

	"github.com/Licoy/sshyard/internal/theme"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

func attachmentTester(t *testing.T, width int, files *[]*aiAttach, removable bool) (*App, *ui.Tester) {
	t.Helper()
	a := &App{winState: testWin()}
	tt := ui.NewTester(func(c *ui.Context) {
		tok := theme.For(theme.Auto, c.Theme().Dark)
		c.SetTheme(tok.Apply(*c.Theme()))
		c.Root().Background(tok.Canvas)
		ui.Column(c).Fill().Padding(theme.Space(2)).Children(func() {
			var remove func(int)
			if removable {
				remove = func(i int) { *files = append((*files)[:i], (*files)[i+1:]...) }
			}
			a.attachStrip(c, tok, locales.In(c.Locale()), *files, remove)
		})
		a.aiAttachmentPreviewView(c, locales.In(c.Locale()), tok)
	}, width, 480)
	tt.SetLocale("en")
	tt.Frame()
	return a, tt
}

func TestAIAttachmentPreview(t *testing.T) {
	for _, removable := range []bool{false, true} {
		t.Run(fmt.Sprintf("composer=%v", removable), func(t *testing.T) {
			img, _ := attachData("shot.png", pngBytes(t))
			files := []*aiAttach{img}
			a, tt := attachmentTester(t, 320, &files, removable)
			if !removable {
				if err := tt.Click("Remove shot.png"); err == nil || len(files) != 1 {
					t.Fatal("sent attachment has remove action")
				}
			}
			for _, close := range []string{"button", "escape", "outside"} {
				mustClick(t, tt, "Preview shot.png")
				tt.Frame()
				if a.attachmentPreview == nil {
					t.Fatal("image preview did not open")
				}
				switch close {
				case "button":
					mustClick(t, tt, "Close Preview")
				case "escape":
					tt.Key(0, ui.KeyEscape)
				case "outside":
					tt.ClickAt(1, 1)
				}
				tt.Frame()
				if a.attachmentPreview != nil {
					t.Fatalf("preview did not close via %s", close)
				}
				tt.Frame()
				if a.attachmentPreview != nil {
					t.Fatal("preview reopened on next frame")
				}
			}
		})
	}
}

func TestAIAttachmentDuplicateNames(t *testing.T) {
	first, _ := attachData("same.png", pngBytes(t))
	second, _ := attachData("same.png", pngBytes(t))
	files := []*aiAttach{first, second}
	a, tt := attachmentTester(t, 640, &files, true)
	mustClick(t, tt, "Remove same.png")
	tt.Frame()
	if len(files) != 1 || files[0] != second {
		t.Fatal("removal did not preserve the other file with the same name")
	}
	mustClick(t, tt, "Preview same.png")
	tt.Frame()
	if a.attachmentPreview == nil || a.attachmentPreview.file != second {
		t.Fatal("remaining duplicate previewed the wrong image")
	}
	mustClick(t, tt, "Close Preview")
	tt.Frame()
	mustClick(t, tt, "Remove same.png")
	tt.Frame()
	if len(files) != 0 {
		t.Fatal("remaining duplicate did not remove")
	}
}
