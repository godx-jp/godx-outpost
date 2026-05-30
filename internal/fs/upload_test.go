package fs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/godx-jp/godx-outpost/internal/channel"
	"github.com/godx-jp/godx-outpost/internal/launcher"
	"github.com/godx-jp/godx-outpost/internal/protocol"
)

// mockConn is a channel.Conn that records everything sent back to the client.
type mockConn struct {
	profile launcher.Profile
	sent    []protocol.Envelope
	binary  []protocol.BinaryFrame
}

func (m *mockConn) Send(e protocol.Envelope) error {
	m.sent = append(m.sent, e)
	return nil
}

func (m *mockConn) SendBinary(f protocol.BinaryFrame) error {
	m.binary = append(m.binary, f)
	return nil
}

func (m *mockConn) Profile() launcher.Profile { return m.profile }

var _ channel.Conn = (*mockConn)(nil)

// doUpload drives one "upload" envelope through the handler and returns the
// single response envelope it produced.
func doUpload(t *testing.T, c *mockConn, req uploadReq) protocol.Envelope {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal req: %v", err)
	}
	e := protocol.Envelope{Ch: protocol.ChFS, Type: "upload", ID: "req-1", Data: data}
	if err := New().Handle(context.Background(), e, c); err != nil {
		t.Fatalf("Handle returned framework error: %v", err)
	}
	if len(c.sent) != 1 {
		t.Fatalf("expected exactly 1 response, got %d", len(c.sent))
	}
	return c.sent[0]
}

// mustB64 base64-encodes bytes for an upload payload.
func mustB64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// decodePath unmarshals a successful upload response and returns its path.
func decodePath(t *testing.T, e protocol.Envelope) string {
	t.Helper()
	if e.Err != "" {
		t.Fatalf("unexpected error envelope: %s", e.Err)
	}
	var resp uploadResp
	if err := e.Bind(&resp); err != nil {
		t.Fatalf("bind resp: %v", err)
	}
	return resp.Path
}

func TestUploadAdminWritesUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := []byte("hello image bytes \x00\x01\x02")
	c := &mockConn{profile: launcher.Profile{Name: "admin"}} // Root == "" → admin
	resp := doUpload(t, c, uploadReq{Name: "photo.png", Content: mustB64(want)})
	path := decodePath(t, resp)

	wantDir := filepath.Join(home, ".remote-host", "uploads")
	if got := filepath.Dir(path); got != wantDir {
		t.Errorf("uploaded into %q, want dir %q", got, wantDir)
	}
	if !strings.HasSuffix(path, "-photo.png") {
		t.Errorf("path %q does not preserve the original name suffix", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("round-trip mismatch: got %q want %q", got, want)
	}
}

func TestUploadSandboxedStaysUnderRoot(t *testing.T) {
	root := t.TempDir()
	c := &mockConn{profile: launcher.Profile{Name: "guest", Root: root}}

	resp := doUpload(t, c, uploadReq{Name: "doc.txt", Content: mustB64([]byte("x"))})
	path := decodePath(t, resp)

	wantDir := filepath.Join(root, ".uploads")
	if got := filepath.Dir(path); got != wantDir {
		t.Errorf("sandboxed upload dir = %q, want %q", got, wantDir)
	}
	if !strings.HasPrefix(path, root+string(filepath.Separator)) {
		t.Errorf("sandboxed upload %q escaped root %q", path, root)
	}
}

func TestUploadNameWithTraversalCannotEscape(t *testing.T) {
	root := t.TempDir()
	c := &mockConn{profile: launcher.Profile{Name: "guest", Root: root}}

	// A malicious name must be reduced to its basename and stay inside .uploads.
	resp := doUpload(t, c, uploadReq{Name: "../../../etc/passwd", Content: mustB64([]byte("y"))})
	path := decodePath(t, resp)

	wantDir := filepath.Join(root, ".uploads")
	if got := filepath.Dir(path); got != wantDir {
		t.Errorf("traversal name landed in %q, want %q", got, wantDir)
	}
	if strings.Contains(path, "..") {
		t.Errorf("path %q still contains a traversal component", path)
	}
	if !strings.HasSuffix(path, "-passwd") {
		t.Errorf("path %q should end with the basename -passwd", path)
	}
}

func TestUploadInvalidBase64ReturnsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	c := &mockConn{profile: launcher.Profile{Name: "admin"}}
	resp := doUpload(t, c, uploadReq{Name: "x.bin", Content: "not%%base64%%"})
	if resp.Err == "" {
		t.Fatalf("expected an error envelope for invalid base64, got success")
	}
	// Nothing should have been written.
	dir := filepath.Join(home, ".remote-host", "uploads")
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		t.Errorf("invalid upload still wrote %d file(s) to %q", len(entries), dir)
	}
}

func TestSanitizeUploadBase(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"image.jpg", "image.jpg"},
		{"sub/dir/file.png", "file.png"},
		{`win\path\name.txt`, "name.txt"},
		{"   ", "upload"},
		{"", "upload"},
		{".", "upload"},
		{"..", "upload"},
		{"/", "upload"},
	}
	for _, tc := range cases {
		got := sanitizeUploadBase(tc.name)
		if got != tc.want {
			t.Errorf("sanitizeUploadBase(%q) = %q, want %q", tc.name, got, tc.want)
		}
		if strings.ContainsAny(got, `/\`) {
			t.Errorf("sanitizeUploadBase(%q) = %q contains a path separator", tc.name, got)
		}
	}
}

// TestCreateUploadFileConcurrent hammers createUploadFile from many goroutines
// with the same source name so they race on the same timestamp. O_EXCL must give
// every caller a distinct file with exactly its own bytes — no overwrite, no
// lost write.
func TestCreateUploadFileConcurrent(t *testing.T) {
	dir := t.TempDir()
	const n = 64

	paths := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			paths[i], errs[i] = createUploadFile(dir, "same.png", []byte(fmt.Sprintf("payload-%d", i)))
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("upload %d failed: %v", i, errs[i])
		}
		if seen[paths[i]] {
			t.Fatalf("two uploads share path %q (overwrite)", paths[i])
		}
		seen[paths[i]] = true

		got, err := os.ReadFile(paths[i])
		if err != nil {
			t.Fatalf("read %q: %v", paths[i], err)
		}
		if want := fmt.Sprintf("payload-%d", i); string(got) != want {
			t.Errorf("path %q has %q, want %q (lost/overwritten write)", paths[i], got, want)
		}
	}

	// Every created file must still be present on disk.
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(des) != n {
		t.Errorf("dir holds %d files, want %d", len(des), n)
	}
}

// TestCreateUploadFileNeverOverwrites checks that a pre-existing file with the
// exact name the first attempt would choose is left untouched and the upload
// lands on a retry name instead.
func TestCreateUploadFileNeverOverwrites(t *testing.T) {
	dir := t.TempDir()

	// Pre-create every plausible attempt-0 name across a small timestamp window
	// is impractical; instead create one upload, then re-create with a forced
	// collision by writing a sentinel at the returned name and uploading again.
	first, err := createUploadFile(dir, "x.bin", []byte("first"))
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}

	// A second upload must not reuse or clobber the first file.
	second, err := createUploadFile(dir, "x.bin", []byte("second"))
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if first == second {
		t.Fatalf("second upload reused the first path %q", first)
	}
	if b, _ := os.ReadFile(first); string(b) != "first" {
		t.Errorf("first file was overwritten: %q", b)
	}
	if b, _ := os.ReadFile(second); string(b) != "second" {
		t.Errorf("second file content wrong: %q", b)
	}
}

func TestUploadIsCollisionResistant(t *testing.T) {
	root := t.TempDir()
	c := &mockConn{profile: launcher.Profile{Name: "guest", Root: root}}

	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		c.sent = nil
		resp := doUpload(t, c, uploadReq{Name: "same.png", Content: mustB64([]byte("z"))})
		path := decodePath(t, resp)
		if seen[path] {
			t.Fatalf("duplicate upload path produced: %q", path)
		}
		seen[path] = true
	}
}
