package app

import (
	"testing"

	"github.com/Licoy/sshyard/internal/icons"
	"github.com/Licoy/sshyard/locales"
	"github.com/egoist/mygo/ui"
)

// TestActionBarOverflow: a bar with room shows its actions and no More
// menu; a narrow one keeps the first, and the others run from its More
// menu, a disabled one staying disabled there.
func TestActionBarOverflow(t *testing.T) {
	a := &App{winState: testWin()}
	width := float32(400)
	var splits, finds, files int
	view := func(c *ui.Context) {
		m := locales.In(c.Locale())
		ui.Box(c).Width(width).Children(func() {
			row := ui.Row(c.Key("row")).FillWidth().Gap(8)
			row.Children(func() {
				ui.Spacer(c)
				a.actionBar(c, "test", row.Bounds().W, 0, 8, []barAction{
					{Key: "find", Button: IconButton{Icon: icons.Search, Label: m.Find(), Size: Tiny}, Do: func() { finds++ }},
					{Key: "split", Button: IconButton{Icon: icons.SplitRight, Label: m.SplitRight(), Size: Tiny}, Do: func() { splits++ }},
					{Key: "files", Button: IconButton{Icon: icons.Folder, Label: m.OpenFilesHere(), Size: Tiny, Disabled: true}, Do: func() { files++ }},
				}, nil, m)
			})
		})
	}
	tt := ui.NewTester(view, 500, 300)
	tt.SetLocale("en")
	tt.Frame()
	tt.Frame()
	if tt.HasText("More Actions") {
		t.Fatal("a wide bar shows More Actions")
	}
	mustClick(t, tt, "Split Right")
	if splits != 1 {
		t.Fatalf("%d splits", splits)
	}

	// Room for the first action and the More button only.
	width = 20 + 8 + 20
	tt.Frame()
	tt.Frame()
	mustClick(t, tt, "Find")
	if finds != 1 {
		t.Fatalf("%d finds", finds)
	}
	mustClick(t, tt, "More Actions")
	tt.Frame()
	mustClick(t, tt, "Split Right")
	tt.Frame()
	if splits != 2 {
		t.Fatalf("Split Right from the menu: %d splits", splits)
	}
	mustClick(t, tt, "More Actions")
	tt.Frame()
	mustClick(t, tt, "Open Files for This Host")
	tt.Frame()
	if files != 0 {
		t.Fatal("a disabled action ran from the menu")
	}
}
