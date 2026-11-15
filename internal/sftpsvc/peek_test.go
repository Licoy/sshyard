package sftpsvc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestPeek lists a folder as List does, a link to a folder being one.
func TestPeek(t *testing.T) {
	c, root := connect(t)
	os.Mkdir(filepath.Join(root, "dir"), 0o755)
	os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644)
	os.Symlink(filepath.Join(root, "dir"), filepath.Join(root, "linked"))
	os.Symlink(filepath.Join(root, "file.txt"), filepath.Join(root, "alias"))

	es, err := c.Peek(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range es {
		got[e.Name] = e.IsDir
	}
	want := map[string]bool{"dir": true, "linked": true, "file.txt": false, "alias": false}
	if len(got) != len(want) {
		t.Fatalf("listed %v", got)
	}
	for n, dir := range want {
		if d, ok := got[n]; !ok || d != dir {
			t.Errorf("%s: listed %v (folder %v), want folder %v", n, ok, d, dir)
		}
	}
	if es[0].Name != "dir" || es[1].Name != "linked" {
		t.Errorf("folders not first: %v", es)
	}
	if _, err := c.Peek(context.Background(), filepath.Join(root, "missing")); err == nil {
		t.Error("peeked into a missing folder")
	}
}
