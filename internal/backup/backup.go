// Package backup creates and restores encrypted, authenticated node-home
// archives. The format is self-contained: no external tools are needed.
package backup

import (
	"archive/tar"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/scrypt"
)

// Magic identifies an ARNE backup.
const magic = "ARNEBK1\n"

// chunkSize is the plaintext size of one encrypted chunk.
const chunkSize = 64 << 10

// ErrCorrupt means the archive failed authentication: wrong passphrase, a
// flipped byte, or a truncated stream.
var ErrCorrupt = errors.New("backup: corrupt archive or wrong passphrase")

// Create writes an encrypted tar of home. Stop the node first: files are read
// without a snapshot.
func Create(home string, w io.Writer, passphrase string) error {
	if passphrase == "" {
		return errors.New("backup: passphrase is required")
	}
	key, salt, err := derive(passphrase, nil)
	if err != nil {
		return err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], chunkSize)
	if _, err := io.WriteString(w, magic); err != nil {
		return err
	}
	if _, err := w.Write(salt); err != nil {
		return err
	}
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	enc := &encWriter{w: w, gcm: gcm}
	tw := tar.NewWriter(enc)
	root := filepath.Clean(home)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr := &tar.Header{
			Name: filepath.ToSlash(rel), Mode: int64(info.Mode().Perm()),
			ModTime: info.ModTime(), Format: tar.FormatPAX,
		}
		switch {
		case d.IsDir():
			hdr.Typeflag = tar.TypeDir
		case info.Mode().IsRegular():
			hdr.Typeflag = tar.TypeReg
			hdr.Size = info.Size()
		default:
			return fmt.Errorf("backup: unsupported file type at %s", rel)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return enc.Close()
}

// Restore decrypts r into home. home must not exist yet; extraction happens in
// a sibling directory and is renamed into place only after the whole archive
// authenticates.
func Restore(home string, r io.Reader, passphrase string) error {
	if passphrase == "" {
		return errors.New("backup: passphrase is required")
	}
	if _, err := os.Stat(home); err == nil {
		return fmt.Errorf("backup: refusing to overwrite %s", home)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var hdr [len(magic)]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil || string(hdr[:]) != magic {
		return errors.New("backup: not an ARNE backup")
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(r, salt); err != nil {
		return fmt.Errorf("backup: truncated header: %w", err)
	}
	var sizeBuf [4]byte
	if _, err := io.ReadFull(r, sizeBuf[:]); err != nil {
		return fmt.Errorf("backup: truncated header: %w", err)
	}
	if got := binary.BigEndian.Uint32(sizeBuf[:]); got != chunkSize {
		return fmt.Errorf("backup: unsupported chunk size %d", got)
	}
	key, _, err := derive(passphrase, salt)
	if err != nil {
		return err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}
	dec := &decReader{r: r, gcm: gcm, buf: nil}
	parent := filepath.Dir(filepath.Clean(home))
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return err
	}
	var tmp string
	for i := 0; tmp == ""; i++ {
		tmp = filepath.Join(parent, fmt.Sprintf(".%s.restore-%d", filepath.Base(home), i))
		if _, err := os.Stat(tmp); err == nil {
			tmp = ""
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := extract(dec, tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	// Drain the stream so the final authenticated marker is checked; a backup
	// that ends early must never look like a successful restore.
	if _, err := io.Copy(io.Discard, dec); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	if !dec.final {
		os.RemoveAll(tmp)
		return ErrCorrupt
	}
	if err := os.Rename(tmp, home); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	return nil
}

func extract(dec *decReader, root string) error {
	tr := tar.NewReader(dec)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		dest, err := safePath(root, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, fs.FileMode(hdr.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
				return err
			}
			f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("backup: unsupported entry %q", hdr.Name)
		}
	}
}

// safePath rejects absolute and parent-escaping archive names.
func safePath(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, `\`) {
		return "", fmt.Errorf("backup: unsafe path %q", name)
	}
	dest := filepath.Join(root, filepath.FromSlash(name))
	rel, err := filepath.Rel(root, dest)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("backup: unsafe path %q", name)
	}
	return dest, nil
}

func derive(passphrase string, salt []byte) ([]byte, []byte, error) {
	if salt == nil {
		salt = make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil, nil, err
		}
	}
	key, err := scrypt.Key([]byte(passphrase), salt, 1<<15, 8, 1, 32)
	if err != nil {
		return nil, nil, err
	}
	return key, salt, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func chunkAAD(index uint64, final bool) []byte {
	b := make([]byte, 9)
	binary.BigEndian.PutUint64(b, index)
	if final {
		b[8] = 1
	}
	return b
}

func writeChunk(w io.Writer, gcm cipher.AEAD, index uint64, final bool, plain []byte) error {
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ct := gcm.Seal(nil, nonce, plain, chunkAAD(index, final))
	var hdr [5]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(ct)))
	if final {
		hdr[4] = 1
	}
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := w.Write(nonce); err != nil {
		return err
	}
	_, err := w.Write(ct)
	return err
}

// encWriter buffers plaintext and emits authenticated chunks.
type encWriter struct {
	w      io.Writer
	gcm    cipher.AEAD
	buf    []byte
	index  uint64
	closed bool
}

func (e *encWriter) Write(p []byte) (int, error) {
	if e.closed {
		return 0, errors.New("backup: write after close")
	}
	e.buf = append(e.buf, p...)
	for len(e.buf) >= chunkSize {
		if err := writeChunk(e.w, e.gcm, e.index, false, e.buf[:chunkSize]); err != nil {
			return 0, err
		}
		e.index++
		e.buf = e.buf[chunkSize:]
	}
	return len(p), nil
}

// Close flushes the tail and appends the authenticated end marker.
func (e *encWriter) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	if len(e.buf) > 0 {
		if err := writeChunk(e.w, e.gcm, e.index, false, e.buf); err != nil {
			return err
		}
		e.index++
	}
	return writeChunk(e.w, e.gcm, e.index, true, nil)
}

// decReader authenticates and yields plaintext chunks. A stream that ends
// without the final marker is corrupt, not a short archive.
type decReader struct {
	r     io.Reader
	gcm   cipher.AEAD
	buf   []byte
	index uint64
	final bool
	err   error
}

func (d *decReader) Read(p []byte) (int, error) {
	for len(d.buf) == 0 {
		if d.err != nil {
			return 0, d.err
		}
		if d.final {
			return 0, io.EOF
		}
		if err := d.next(); err != nil {
			d.err = err
			return 0, err
		}
	}
	n := copy(p, d.buf)
	d.buf = d.buf[n:]
	return n, nil
}

func (d *decReader) next() error {
	var hdr [5]byte
	if _, err := io.ReadFull(d.r, hdr[:]); err != nil {
		return fmt.Errorf("backup: truncated archive: %w", ErrCorrupt)
	}
	n := binary.BigEndian.Uint32(hdr[:4])
	flags := hdr[4]
	if flags > 1 || n > chunkSize+uint32(d.gcm.Overhead()) {
		return ErrCorrupt
	}
	final := flags == 1
	nonce := make([]byte, d.gcm.NonceSize())
	if _, err := io.ReadFull(d.r, nonce); err != nil {
		return ErrCorrupt
	}
	ct := make([]byte, n)
	if _, err := io.ReadFull(d.r, ct); err != nil {
		return ErrCorrupt
	}
	plain, err := d.gcm.Open(nil, nonce, ct, chunkAAD(d.index, final))
	if err != nil {
		return ErrCorrupt
	}
	d.index++
	if final {
		if len(plain) != 0 {
			return ErrCorrupt
		}
		d.final = true
		return nil
	}
	if len(plain) == 0 {
		return ErrCorrupt
	}
	d.buf = plain
	return nil
}
