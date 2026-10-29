package backup

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func writeTree(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "app", "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"config/genesis.json": `{"chain_id":"arne-devnet-1"}`,
		"app/state.bin":       string([]byte{0, 1, 2, 255}),
		"app/nested/note.txt": "hello",
	}
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	src := filepath.Join(t.TempDir(), "home")
	writeTree(t, src)
	var buf bytes.Buffer
	if err := Create(src, &buf, "correct horse"); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "restored")
	if err := Restore(dst, &buf, "correct horse"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "app", "state.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{0, 1, 2, 255}) {
		t.Fatalf("state = %v", got)
	}
	if body, _ := os.ReadFile(filepath.Join(dst, "app", "nested", "note.txt")); string(body) != "hello" {
		t.Fatalf("note = %q", body)
	}
	// Restore must not silently overwrite an existing home.
	if err := Restore(dst, &buf, "correct horse"); err == nil {
		t.Fatal("existing target overwritten")
	}
}

func TestWrongPassphrase(t *testing.T) {
	src := filepath.Join(t.TempDir(), "home")
	writeTree(t, src)
	var buf bytes.Buffer
	if err := Create(src, &buf, "one"); err != nil {
		t.Fatal(err)
	}
	if err := Restore(filepath.Join(t.TempDir(), "out"), &buf, "two"); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
}

func TestCorruptAndTruncated(t *testing.T) {
	src := filepath.Join(t.TempDir(), "home")
	writeTree(t, src)
	var buf bytes.Buffer
	if err := Create(src, &buf, "pass"); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()

	corrupt := append([]byte(nil), data...)
	corrupt[len(corrupt)-1] ^= 1
	if err := Restore(filepath.Join(t.TempDir(), "a"), bytes.NewReader(corrupt), "pass"); err == nil {
		t.Fatal("corrupt archive accepted")
	}

	truncated := data[:len(data)-3]
	if err := Restore(filepath.Join(t.TempDir(), "b"), bytes.NewReader(truncated), "pass"); err == nil {
		t.Fatal("truncated archive accepted")
	}

	// Dropping the whole end marker must fail, not restore a prefix.
	noMarker := data[:len(data)-(5+12+16)]
	if err := Restore(filepath.Join(t.TempDir(), "c"), bytes.NewReader(noMarker), "pass"); err == nil {
		t.Fatal("archive without end marker accepted")
	}
}

func TestUnsafePathsRejected(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../evil", "/abs/path", `..\evil`, "a/../../b"} {
		if _, err := safePath(root, name); err == nil {
			t.Fatalf("unsafe path %q accepted", name)
		}
	}
	if _, err := safePath(root, "app/state"); err != nil {
		t.Fatalf("safe path rejected: %v", err)
	}
}

func TestShortPassphraseHeader(t *testing.T) {
	if err := Restore(filepath.Join(t.TempDir(), "x"), bytes.NewReader([]byte("garbage")), "pass"); err == nil {
		t.Fatal("non-backup accepted")
	}
	if err := Create(t.TempDir(), io.Discard, ""); err == nil {
		t.Fatal("empty passphrase accepted")
	}
}
