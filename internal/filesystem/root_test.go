package filesystem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootReadTreeAndTraversal(t *testing.T) {
	d := t.TempDir()
	os.Mkdir(filepath.Join(d, "src"), 0755)
	os.WriteFile(filepath.Join(d, "src", "a.txt"), []byte("hello"), 0644)
	os.Mkdir(filepath.Join(d, "node_modules"), 0755)
	os.WriteFile(filepath.Join(d, "node_modules", "x"), []byte("x"), 0644)
	r, e := New(d, []string{"node_modules"}, 1024)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Read("../secret"); e == nil {
		t.Fatal("accepted traversal")
	}
	b, e := r.Read("src/a.txt")
	if e != nil || string(b) != "hello" {
		t.Fatalf("read: %q %v", b, e)
	}
	tree, e := r.Tree()
	if e != nil || len(tree.Children) != 1 || tree.Children[0].Name != "src" {
		t.Fatalf("tree: %#v %v", tree, e)
	}
}
func TestRejectExternalSymlink(t *testing.T) {
	d := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte("secret"), 0600)
	if e := os.Symlink(outside, filepath.Join(d, "escape")); e != nil {
		t.Skip(e)
	}
	r, e := New(d, nil, 100)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Read("escape"); e != ErrOutsideRoot {
		t.Fatalf("got %v", e)
	}
}
func TestMaxSizeAndBinary(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "large.txt"), []byte("12345"), 0644)
	os.WriteFile(filepath.Join(d, "x.bin"), []byte{'a', 0, 'b'}, 0644)
	r, _ := New(d, nil, 4)
	if _, e := r.Read("large.txt"); e == nil {
		t.Fatal("expected size rejection")
	}
	if _, e := r.Read("x.bin"); e != ErrNotText {
		t.Fatalf("got %v", e)
	}
}
