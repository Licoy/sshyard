package sftpsvc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/sshtest"
)

// enqueueRelay makes the plan's folders on the destination and queues
// its files from the source, as the app does.
func enqueueRelay(t *testing.T, from func(context.Context) (*Client, error), to *Client, ts *Transfers, items []Item) []int {
	t.Helper()
	var ids []int
	for _, it := range items {
		if it.IsDir {
			if err := to.MkdirAll(it.Dst); err != nil {
				t.Fatal(err)
			}
			continue
		}
		ids = append(ids, ts.AddRelay(from, "source", it.Src, it.Dst, it.Rel))
	}
	return ids
}

// parts lists the part files left under root.
func parts(root string) []string {
	var out []string
	filepath.Walk(root, func(p string, _ os.FileInfo, _ error) error {
		if strings.Contains(filepath.Base(p), ".sshyard-part") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// A folder copied from one server to another arrives whole, with the
// files' modes and times, links handled as downloads handle them, and
// nothing written on this computer meanwhile.
func TestRelayCopiesTree(t *testing.T) {
	scratch := t.TempDir()
	src, srcRoot := queueServer(t)
	dst, dstRoot := queueServer(t)
	tree(t, srcRoot, "proj")
	exe := filepath.Join(srcRoot, "proj", "run.sh")
	os.WriteFile(exe, []byte("#!/bin/sh\necho hi\n"), 0o750)
	when := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	os.Chtimes(exe, when, when)
	os.Symlink("run.sh", filepath.Join(srcRoot, "proj", "link.sh"))
	os.Symlink("a0", filepath.Join(srcRoot, "proj", "linkdir"))
	os.Symlink("nowhere", filepath.Join(srcRoot, "proj", "broken"))
	os.Mkdir(filepath.Join(dstRoot, "in"), 0o755)
	// Nothing goes through this computer's temporary folder.
	t.Setenv("TMPDIR", scratch)

	from, err := src.get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	to, err := dst.get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	items, skips, err := from.PlanRelay(context.Background(), []string{srcRoot + "/proj"}, to, dstRoot+"/in")
	if err != nil {
		t.Fatal(err)
	}
	var sk []string
	for _, s := range skips {
		sk = append(sk, fmt.Sprintf("%s:%d", s.Rel, s.Reason))
	}
	if want := fmt.Sprint([]string{fmt.Sprintf("proj/broken:%d", SkipBrokenLink), fmt.Sprintf("proj/linkdir:%d", SkipLinkedFolder)}); fmt.Sprint(sk) != want {
		t.Fatalf("skips %v, want %v", sk, want)
	}
	for _, it := range items {
		if !RemoteWithin(dstRoot+"/in", it.Dst) || it.Exists {
			t.Fatalf("%+v", it)
		}
	}
	ts := newQueue(dst)
	ids := enqueueRelay(t, src.get, to, ts, items)
	ts.Wait()
	for _, tr := range ts.List() {
		if tr.State != Done || tr.Dir != Relay || tr.From != "source" {
			t.Fatalf("%+v", tr)
		}
	}
	if len(ids) != 202 {
		t.Fatalf("%d files queued", len(ids))
	}
	want := snapshot(t, filepath.Join(srcRoot, "proj"))
	delete(want, "broken")
	delete(want, "linkdir")
	want["link.sh"] = want["run.sh"] // the link's file, as a download keeps it
	got := snapshot(t, filepath.Join(dstRoot, "in", "proj"))
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("copy differs:\n got %v\nwant %v", got, want)
	}
	fi, err := os.Lstat(filepath.Join(dstRoot, "in", "proj", "run.sh"))
	if err != nil || fi.Mode().Perm() != 0o750 || !fi.ModTime().Equal(when) {
		t.Fatalf("run.sh: %v %v %v", fi.Mode(), fi.ModTime(), err)
	}
	if fi, _ := os.Lstat(filepath.Join(dstRoot, "in", "proj", "a0", "b0", "c", "f0.log")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("f0.log mode %v", fi.Mode())
	}
	if p := parts(dstRoot); len(p) != 0 {
		t.Fatalf("part files left: %v", p)
	}
	if left, _ := os.ReadDir(scratch); len(left) != 0 {
		t.Fatalf("written locally: %v", left)
	}
}

// What is already there: the plan says so; replacing renames the copy
// over it, keeping both writes beside it, skipping leaves it.
func TestRelayConflicts(t *testing.T) {
	src, srcRoot := queueServer(t)
	dst, dstRoot := queueServer(t)
	os.WriteFile(filepath.Join(srcRoot, "a.txt"), []byte("new a"), 0o644)
	os.WriteFile(filepath.Join(srcRoot, "b.txt"), []byte("new b"), 0o644)
	os.WriteFile(filepath.Join(srcRoot, "c.txt"), []byte("new c"), 0o644)
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		os.WriteFile(filepath.Join(dstRoot, n), []byte("old"), 0o644)
	}
	from, _ := src.get(context.Background())
	to, _ := dst.get(context.Background())
	items, _, err := from.PlanRelay(context.Background(), []string{srcRoot + "/a.txt", srcRoot + "/b.txt", srcRoot + "/c.txt"}, to, dstRoot)
	if err != nil || len(items) != 3 {
		t.Fatalf("%+v %v", items, err)
	}
	for _, it := range items {
		if !it.Exists {
			t.Fatalf("%+v", it)
		}
	}
	ts := newQueue(dst)
	ts.AddRelay(src.get, "source", items[0].Src, items[0].Dst, "a.txt") // replace
	both := UniqueName(items[1].Dst, true, to.Exists)
	ts.AddRelay(src.get, "source", items[1].Src, both, "b.txt") // keep both
	// c.txt skipped: nothing queued
	ts.Wait()
	for n, want := range map[string]string{"a.txt": "new a", "b.txt": "old", "b (2).txt": "new b", "c.txt": "old"} {
		if got, _ := os.ReadFile(filepath.Join(dstRoot, n)); string(got) != want {
			t.Errorf("%s: %q, want %q", n, got, want)
		}
	}
	if p := parts(dstRoot); len(p) != 0 {
		t.Fatalf("part files left: %v", p)
	}
}

// A relay canceled mid-stream leaves neither the file nor its part on
// the destination; retried, it copies from the start.
func TestRelayCancelLeavesNothing(t *testing.T) {
	src, srcRoot := queueServer(t)
	dst, dstRoot := queueServer(t)
	data := bigFile(t, filepath.Join(srcRoot, "big.bin"), 6<<20)
	ts := newQueue(dst)
	var once sync.Once
	var id int
	ts.onWrite = func(tr *Transfer, done int64) {
		if done > 2<<20 {
			once.Do(func() { go ts.Cancel(id) })
			time.Sleep(time.Millisecond)
		}
	}
	final := dstRoot + "/sub/big.bin"
	id = ts.AddRelay(src.get, "source", srcRoot+"/big.bin", final, "big.bin")
	waitState(t, ts, id, Canceled)
	ts.Wait()
	time.Sleep(100 * time.Millisecond) // the part goes after the run ends
	if LocalExists(filepath.Join(dstRoot, "sub", "big.bin")) {
		t.Fatal("a canceled relay left its file")
	}
	if p := parts(dstRoot); len(p) != 0 {
		t.Fatalf("part files left: %v", p)
	}
	ts.onWrite = nil
	ts.Retry(id)
	ts.Wait()
	tr := ts.List()[0]
	if tr.State != Done || tr.Offset != 0 {
		t.Fatalf("%+v", tr)
	}
	got, _ := os.ReadFile(filepath.Join(dstRoot, "sub", "big.bin"))
	if sha256.Sum256(got) != sha256.Sum256(data) {
		t.Fatal("content differs after the retry")
	}
}

// A paused relay keeps its part on the destination and goes on from it.
func TestRelayPauseResumesAtOffset(t *testing.T) {
	src, srcRoot := queueServer(t)
	dst, dstRoot := queueServer(t)
	data := bigFile(t, filepath.Join(srcRoot, "big.bin"), 8<<20)
	ts := newQueue(dst)
	var once sync.Once
	var id int
	ts.onWrite = func(tr *Transfer, done int64) {
		if done >= int64(len(data))*4/10 {
			once.Do(func() { go ts.Pause(id) })
			time.Sleep(time.Millisecond)
		}
	}
	id = ts.AddRelay(src.get, "source", srcRoot+"/big.bin", dstRoot+"/big.bin", "big.bin")
	waitState(t, ts, id, Paused)
	ts.Wait()
	tr := ts.List()[0]
	if tr.State != Paused || tr.Bytes <= 0 || tr.Bytes >= int64(len(data)) {
		t.Fatalf("%+v", tr)
	}
	if LocalExists(filepath.Join(dstRoot, "big.bin")) || len(parts(dstRoot)) != 1 {
		t.Fatalf("paused: %v", parts(dstRoot))
	}
	ts.onWrite = nil
	ts.Resume(id)
	ts.Wait()
	tr = ts.List()[0]
	if tr.State != Done || tr.Offset <= 0 {
		t.Fatalf("resumed: %+v", tr)
	}
	got, _ := os.ReadFile(filepath.Join(dstRoot, "big.bin"))
	if !bytes.Equal(got, data) {
		t.Fatal("content differs after resume")
	}
	if p := parts(dstRoot); len(p) != 0 {
		t.Fatalf("part files left: %v", p)
	}
}

// The source server dropping mid-stream is retried by itself, from what
// already reached the destination.
func TestRelayRetriesAfterSourceDrop(t *testing.T) {
	src, srcRoot := queueServer(t)
	dst, dstRoot := queueServer(t)
	data := bigFile(t, filepath.Join(srcRoot, "big.bin"), 6<<20)
	ts := newQueue(dst)
	var once sync.Once
	ts.onWrite = func(tr *Transfer, done int64) {
		if done > 2<<20 {
			once.Do(src.srv.Drop)
		}
	}
	ts.AddRelay(src.get, "source", srcRoot+"/big.bin", dstRoot+"/big.bin", "big.bin")
	ts.Wait()
	tr := ts.List()[0]
	if tr.State != Done || tr.Attempt < 1 || tr.Offset <= 0 {
		t.Fatalf("%+v", tr)
	}
	got, _ := os.ReadFile(filepath.Join(dstRoot, "big.bin"))
	if !bytes.Equal(got, data) {
		t.Fatal("content differs after the retry")
	}
	if src.n.Load() < 2 {
		t.Error("the source was never dialed again")
	}
}

// Names a hostile source sends that would leave the folder chosen on the
// destination are left out of the plan; a relay queued to one anyway
// fails at once, writing nothing.
func TestRelayHostileNames(t *testing.T) {
	ffs := &sshtest.FakeFS{
		Dirs: map[string][]sshtest.FakeEntry{
			"/":     {{Name: "evil", Dir: true}},
			"/evil": {{Name: "ok.txt", Size: 2}, {Name: `..\..\win.txt`, Size: 3}, {Name: "../", Size: 4}, {Name: "10:30 aux.c", Size: 1}},
		},
		Files: map[string][]byte{"/evil/ok.txt": []byte("ok"), `/evil/..\..\win.txt`: []byte("pwn"), "/evil/10:30 aux.c": []byte("c")},
	}
	from := connectFake(t, ffs)
	dst, dstRoot := queueServer(t)
	in := filepath.Join(dstRoot, "in")
	os.Mkdir(in, 0o755)
	to, _ := dst.get(context.Background())
	items, skips, err := from.PlanRelay(context.Background(), []string{"/evil"}, to, in)
	if err != nil {
		t.Fatal(err)
	}
	// Names legal on servers stay, unlike in a download.
	if got := fmt.Sprint(rels(items)); got != "[evil evil/10:30 aux.c evil/ok.txt]" {
		t.Fatalf("items %s", got)
	}
	if len(skips) != 1 || skips[0].Rel != `evil/..\..\win.txt` || skips[0].Reason != SkipName {
		t.Fatalf("skips %+v", skips)
	}
	ts := newQueue(dst)
	get := func(context.Context) (*Client, error) { return from, nil }
	enqueueRelay(t, get, to, ts, items)
	ts.Wait()
	for _, tr := range ts.List() {
		if tr.State != Done {
			t.Fatalf("%+v", tr)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(in, "evil", "ok.txt")); string(got) != "ok" {
		t.Fatalf("ok.txt %q", got)
	}
	// Queued to unsafe names anyway: refused before any part file.
	ts = newQueue(dst)
	for _, dst := range []string{in + `/..\..\win.txt`, in + "/x/..", in + "/.."} {
		id := ts.AddRelay(get, "source", "/evil/ok.txt", dst, "x")
		tr := waitState(t, ts, id, Failed)
		if !errors.Is(tr.Err, ErrUnsafeRemoteName) || tr.Attempt != 0 {
			t.Fatalf("%s: %+v", dst, tr)
		}
	}
	if left := files(dstRoot); len(left) != 2 {
		t.Fatalf("files: %v", left)
	}
}

func TestRemotePathAndInto(t *testing.T) {
	if p, err := RemotePath("/srv/in", "a/b.txt"); err != nil || p != "/srv/in/a/b.txt" {
		t.Fatalf("%q %v", p, err)
	}
	if p, err := RemotePath("/", "10:30 aux.c"); err != nil || p != "/10:30 aux.c" {
		t.Fatalf("%q %v", p, err)
	}
	for _, rel := range []string{"", "..", "../x", "a/../../x", `..\x`, "a//b", "/etc/passwd", "a/./b", "a\x00b"} {
		if p, err := RemotePath("/srv/in", rel); !errors.Is(err, ErrUnsafeRemoteName) {
			t.Errorf("%q: %q %v, want ErrUnsafeRemoteName", rel, p, err)
		}
	}
	if !RemoteWithin("/srv", "/srv/a") || RemoteWithin("/srv", "/srvx") || !RemoteWithin("/", "/a") || RemoteWithin("/srv/a", "/srv") {
		t.Fatal("RemoteWithin")
	}
	for _, dir := range []string{"/srv/a", "/srv/a/b", "/srv/a/"} {
		if err := CheckInto([]string{"/srv/x", "/srv/a"}, dir); !errors.Is(err, ErrIntoItself) {
			t.Errorf("%s: %v", dir, err)
		}
	}
	for _, dir := range []string{"/srv", "/srv/ab", "/other"} {
		if err := CheckInto([]string{"/srv/a"}, dir); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
	}
}

// A server without SFTP, served by scp, relays both ways like any other.
func TestRelaySCPFallback(t *testing.T) {
	scp, scpRoot := connectNoSFTP(t)
	dst, dstRoot := queueServer(t)
	to, _ := dst.get(context.Background())
	os.WriteFile(filepath.Join(scpRoot, "from scp.txt"), []byte("by scp"), 0o640)
	os.WriteFile(filepath.Join(dstRoot, "to scp.txt"), []byte("to scp"), 0o600)
	getSCP := func(context.Context) (*Client, error) { return scp, nil }

	// scp to SFTP
	items, _, err := scp.PlanRelay(context.Background(), []string{scpRoot + "/from scp.txt"}, to, dstRoot)
	if err != nil || len(items) != 1 {
		t.Fatalf("%+v %v", items, err)
	}
	ts := newQueue(dst)
	enqueueRelay(t, getSCP, to, ts, items)
	ts.Wait()
	if got, _ := os.ReadFile(filepath.Join(dstRoot, "from scp.txt")); string(got) != "by scp" {
		t.Fatalf("%q %+v", got, ts.List())
	}

	// SFTP to scp
	items, _, err = to.PlanRelay(context.Background(), []string{dstRoot + "/to scp.txt"}, scp, scpRoot)
	if err != nil || len(items) != 1 {
		t.Fatalf("%+v %v", items, err)
	}
	back := &Transfers{Client: getSCP, Logf: func(string, ...any) {}, Backoff: func(int) time.Duration { return 10 * time.Millisecond }}
	enqueueRelay(t, dst.get, scp, back, items)
	back.Wait()
	if got, _ := os.ReadFile(filepath.Join(scpRoot, "to scp.txt")); string(got) != "to scp" {
		t.Fatalf("%q %+v", got, back.List())
	}
	if p := parts(scpRoot); len(p) != 0 {
		t.Fatalf("part files left: %v", p)
	}
}
