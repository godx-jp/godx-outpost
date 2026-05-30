// Package fs implements the FILE channel (protocol.ChFS).
//
// Handler routes inbound Envelope messages to file-system operations and sends
// back response envelopes correlated by the same ID. Large files are streamed
// as BinFSData binary frames instead of being base64-encoded inline.
//
// Security: when the authenticated Profile has a non-empty Root field the
// handler treats it as a chroot-like boundary. All paths are cleaned and
// re-joined under Root; any path that escapes the boundary (via ".." or an
// absolute component) is rejected. When Root is empty (admin profile) the
// handler operates on the real filesystem with only leading "~" expansion.
package fs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godx-jp/godx-outpost/internal/channel"
	"github.com/godx-jp/godx-outpost/internal/protocol"
)

// largFileThreshold is the byte size above which "read" responses are streamed
// as BinFSData frames rather than embedded as base64 in a JSON envelope.
const largeFileThreshold = 512 * 1024 // 512 KiB

// chunkSize is the payload size for each BinFSData binary frame.
const chunkSize = 32 * 1024 // 32 KiB

// Handler implements channel.Handler for protocol.ChFS.
type Handler struct{}

// New returns a new file-system channel Handler.
func New() *Handler { return &Handler{} }

// Channel returns the channel identifier this handler owns.
func (h *Handler) Channel() protocol.Channel { return protocol.ChFS }

// Close is a no-op for the fs handler (no per-connection state to release).
func (h *Handler) Close() error { return nil }

// Handle dispatches a single inbound envelope to the appropriate fs operation.
// Errors are sent back as error envelopes; the function itself only returns an
// error for unexpected framework-level failures.
func (h *Handler) Handle(ctx context.Context, e protocol.Envelope, c channel.Conn) error {
	switch e.Type {
	case "list":
		return h.handleList(ctx, e, c)
	case "stat":
		return h.handleStat(ctx, e, c)
	case "read":
		return h.handleRead(ctx, e, c)
	case "write":
		return h.handleWrite(ctx, e, c)
	case "upload":
		return h.handleUpload(ctx, e, c)
	case "mkdir":
		return h.handleMkdir(ctx, e, c)
	case "delete":
		return h.handleDelete(ctx, e, c)
	default:
		return c.Send(protocol.ErrorEnvelope(protocol.ChFS, e.Type, e.ID,
			fmt.Sprintf("fs: unknown type %q", e.Type)))
	}
}

// ---- request/response types -------------------------------------------------

// pathReq is the request payload for operations that take only a path.
type pathReq struct {
	Path string `json:"path"`
}

// writeReq is the request payload for the "write" operation.
type writeReq struct {
	Path    string `json:"path"`
	Content string `json:"content"` // base64-encoded
}

// uploadReq is the request payload for the "upload" operation. Unlike "write",
// the client does not choose the destination path: the handler picks a unique
// name inside the app-managed uploads directory and returns the resulting host
// path, which the caller can then paste into a terminal session.
type uploadReq struct {
	Name    string `json:"name"`    // original file name (basename only is used)
	Content string `json:"content"` // base64-encoded
}

// uploadResp is the response payload for "upload": the absolute host path the
// bytes were written to.
type uploadResp struct {
	Path string `json:"path"`
}

// entry is a directory entry or stat result.
type entry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"` // os.FileMode.String()
	IsDir   bool   `json:"isDir"`
	ModTime int64  `json:"modTime"` // Unix seconds
}

// listResp is the response payload for "list".
type listResp struct {
	Entries []entry `json:"entries"`
}

// readResp is the response payload for small-file "read".
type readResp struct {
	Content string `json:"content"` // base64-encoded
}

// okResp is the generic success payload.
type okResp struct {
	OK bool `json:"ok"`
}

// ---- helpers ----------------------------------------------------------------

// resolvePath converts a client-supplied path to a real filesystem path while
// enforcing any chroot boundary from the profile.
//
// Rules:
//  1. Expand a leading "~" to the current user's home directory.
//  2. Make the path absolute (relative paths anchored to home).
//  3. If profile.Root != "", clean-join under Root and reject any ".." escape.
func resolvePath(raw string, root string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("fs: path must not be empty")
	}

	// Expand leading ~.
	raw, err := expandTilde(raw)
	if err != nil {
		return "", err
	}

	// If root is set, enforce the boundary.
	if root != "" {
		cleanRoot, err := resolveRoot(root)
		if err != nil {
			return "", err
		}

		// Strip any absolute prefix from the client path so it is always
		// treated as relative to root.
		cleaned := filepath.Clean(raw)
		if filepath.IsAbs(cleaned) {
			// Strip the leading slash so Join treats it as relative.
			cleaned = strings.TrimPrefix(cleaned, "/")
		}

		joined := filepath.Join(cleanRoot, cleaned)
		// Lexical check: still inside root (no ".." escape).
		if !withinRoot(joined, cleanRoot) {
			return "", fmt.Errorf("fs: path escapes sandbox root")
		}
		// Symlink check: a symlink *inside* root could point outside it. If the
		// target (or, for not-yet-created paths, its nearest existing ancestor)
		// resolves outside root after following links, reject. (Full TOCTOU-safe
		// sandboxing — openat2/O_NOFOLLOW — is M5 work; this closes the common
		// existing-symlink escape.)
		if real, err := evalNearestExisting(joined); err == nil && !withinRoot(real, cleanRoot) {
			return "", fmt.Errorf("fs: path escapes sandbox root via symlink")
		}
		return joined, nil
	}

	// Admin (no root): just clean and return absolute path.
	if !filepath.IsAbs(raw) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("fs: cannot determine home directory: %w", err)
		}
		raw = filepath.Join(home, raw)
	}
	return filepath.Clean(raw), nil
}

// expandTilde expands a leading "~" or "~/" to the current user's home dir.
func expandTilde(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("fs: cannot determine home directory: %w", err)
		}
		return home + p[1:], nil
	}
	return p, nil
}

// resolveRoot expands and cleans a profile's sandbox root.
func resolveRoot(root string) (string, error) {
	r, err := expandTilde(root)
	if err != nil {
		return "", err
	}
	return filepath.Clean(r), nil
}

// withinRoot reports whether the cleaned path p is root itself or lies under it.
func withinRoot(p, cleanRoot string) bool {
	return p == cleanRoot || strings.HasPrefix(p, cleanRoot+string(filepath.Separator))
}

// evalNearestExisting resolves symlinks on p, or — if p does not exist yet — on
// its nearest existing ancestor, returning the real path. Used to detect a
// symlinked component that would escape the sandbox.
func evalNearestExisting(p string) (string, error) {
	for cur := p; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			if cur == p {
				return real, nil
			}
			// p itself doesn't exist; return real-ancestor joined with the
			// remaining (non-existent) suffix so the prefix check stays valid.
			return filepath.Join(real, strings.TrimPrefix(p, cur)), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("fs: no existing ancestor")
		}
		cur = parent
	}
}

// sendErr sends an error envelope back to the client.
func sendErr(c channel.Conn, typ, id, msg string) error {
	return c.Send(protocol.ErrorEnvelope(protocol.ChFS, typ, id, msg))
}

// sendOK sends a success envelope with the given data.
func sendOK(c channel.Conn, typ, id string, data any) error {
	env, err := protocol.NewEnvelope(protocol.ChFS, typ, id, data)
	if err != nil {
		return err
	}
	return c.Send(env)
}

// infoToEntry converts an os.FileInfo to an entry struct.
func infoToEntry(name string, fi os.FileInfo) entry {
	return entry{
		Name:    name,
		Size:    fi.Size(),
		Mode:    fi.Mode().String(),
		IsDir:   fi.IsDir(),
		ModTime: fi.ModTime().Unix(),
	}
}

// bindPathReq decodes the path from an envelope's Data into a pathReq.
func bindPathReq(e protocol.Envelope) (pathReq, error) {
	var req pathReq
	if err := json.Unmarshal(e.Data, &req); err != nil {
		return pathReq{}, fmt.Errorf("fs: invalid request: %w", err)
	}
	return req, nil
}

// ---- operation handlers -----------------------------------------------------

func (h *Handler) handleList(_ context.Context, e protocol.Envelope, c channel.Conn) error {
	req, err := bindPathReq(e)
	if err != nil {
		return sendErr(c, e.Type, e.ID, err.Error())
	}

	root := c.Profile().Root
	abs, err := resolvePath(req.Path, root)
	if err != nil {
		return sendErr(c, e.Type, e.ID, err.Error())
	}

	des, err := os.ReadDir(abs)
	if err != nil {
		return sendErr(c, e.Type, e.ID, fmt.Sprintf("fs: list %s: %v", abs, err))
	}

	entries := make([]entry, 0, len(des))
	for _, de := range des {
		fi, ferr := de.Info()
		if ferr != nil {
			continue // skip unreadable entries silently
		}
		entries = append(entries, infoToEntry(de.Name(), fi))
	}
	return sendOK(c, e.Type, e.ID, listResp{Entries: entries})
}

func (h *Handler) handleStat(_ context.Context, e protocol.Envelope, c channel.Conn) error {
	req, err := bindPathReq(e)
	if err != nil {
		return sendErr(c, e.Type, e.ID, err.Error())
	}

	root := c.Profile().Root
	abs, err := resolvePath(req.Path, root)
	if err != nil {
		return sendErr(c, e.Type, e.ID, err.Error())
	}

	fi, err := os.Stat(abs)
	if err != nil {
		return sendErr(c, e.Type, e.ID, fmt.Sprintf("fs: stat %s: %v", abs, err))
	}

	return sendOK(c, e.Type, e.ID, infoToEntry(fi.Name(), fi))
}

func (h *Handler) handleRead(ctx context.Context, e protocol.Envelope, c channel.Conn) error {
	req, err := bindPathReq(e)
	if err != nil {
		return sendErr(c, e.Type, e.ID, err.Error())
	}

	root := c.Profile().Root
	abs, err := resolvePath(req.Path, root)
	if err != nil {
		return sendErr(c, e.Type, e.ID, err.Error())
	}

	fi, err := os.Stat(abs)
	if err != nil {
		return sendErr(c, e.Type, e.ID, fmt.Sprintf("fs: read %s: %v", abs, err))
	}
	if fi.IsDir() {
		return sendErr(c, e.Type, e.ID, "fs: read: path is a directory")
	}
	// Only regular files: reading a FIFO, device, or socket can block the
	// reader indefinitely (e.g. a named pipe with no writer), which would
	// otherwise wedge a streaming goroutine that cannot observe ctx
	// cancellation mid-syscall.
	if !fi.Mode().IsRegular() {
		return sendErr(c, e.Type, e.ID, "fs: read: not a regular file")
	}

	// Small files: embed as base64 in the response envelope.
	if fi.Size() <= largeFileThreshold {
		data, rerr := os.ReadFile(abs)
		if rerr != nil {
			return sendErr(c, e.Type, e.ID, fmt.Sprintf("fs: read %s: %v", abs, rerr))
		}
		return sendOK(c, e.Type, e.ID, readResp{Content: base64.StdEncoding.EncodeToString(data)})
	}

	// Large files: stream as BinFSData frames in a goroutine so we do not
	// block the connection's read loop.
	go func() {
		streamID := e.ID

		f, ferr := os.Open(abs)
		if ferr != nil {
			_ = sendErr(c, e.Type, e.ID, fmt.Sprintf("fs: open %s: %v", abs, ferr))
			return
		}
		defer f.Close()

		buf := make([]byte, chunkSize)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			n, rerr := f.Read(buf)
			if n > 0 {
				frame := protocol.BinaryFrame{
					Kind:     protocol.BinFSData,
					StreamID: streamID,
					Payload:  append([]byte(nil), buf[:n]...),
				}
				if serr := c.SendBinary(frame); serr != nil {
					return
				}
			}
			if rerr == io.EOF {
				// Send an empty terminal frame to signal completion.
				_ = c.SendBinary(protocol.BinaryFrame{
					Kind:     protocol.BinFSData,
					StreamID: streamID,
					Payload:  nil,
				})
				return
			}
			if rerr != nil {
				_ = sendErr(c, e.Type, e.ID, fmt.Sprintf("fs: read stream %s: %v", abs, rerr))
				return
			}
		}
	}()

	return nil
}

func (h *Handler) handleWrite(_ context.Context, e protocol.Envelope, c channel.Conn) error {
	var req writeReq
	if err := json.Unmarshal(e.Data, &req); err != nil {
		return sendErr(c, e.Type, e.ID, fmt.Sprintf("fs: invalid request: %v", err))
	}

	root := c.Profile().Root
	abs, err := resolvePath(req.Path, root)
	if err != nil {
		return sendErr(c, e.Type, e.ID, err.Error())
	}

	data, err := base64.StdEncoding.DecodeString(req.Content)
	if err != nil {
		return sendErr(c, e.Type, e.ID, fmt.Sprintf("fs: write: invalid base64 content: %v", err))
	}

	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return sendErr(c, e.Type, e.ID, fmt.Sprintf("fs: write %s: %v", abs, err))
	}

	return sendOK(c, e.Type, e.ID, okResp{OK: true})
}

// handleUpload writes client-supplied bytes into the app-managed uploads
// directory under a unique, sanitized name and returns the resulting host path.
//
// The path is anchored to the connection's sandbox root when the profile has
// one (so a sandboxed client cannot drop files outside its boundary), and to
// ~/.remote-host/uploads for admin profiles. The returned path is absolute so
// the caller can paste it straight into a terminal running on the same host.
func (h *Handler) handleUpload(_ context.Context, e protocol.Envelope, c channel.Conn) error {
	var req uploadReq
	if err := json.Unmarshal(e.Data, &req); err != nil {
		return sendErr(c, e.Type, e.ID, fmt.Sprintf("fs: invalid request: %v", err))
	}

	data, err := base64.StdEncoding.DecodeString(req.Content)
	if err != nil {
		return sendErr(c, e.Type, e.ID, fmt.Sprintf("fs: upload: invalid base64 content: %v", err))
	}

	dir, err := uploadsDir(c.Profile().Root)
	if err != nil {
		return sendErr(c, e.Type, e.ID, err.Error())
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return sendErr(c, e.Type, e.ID, fmt.Sprintf("fs: upload: mkdir %s: %v", dir, err))
	}

	abs, err := createUploadFile(dir, req.Name, data)
	if err != nil {
		return sendErr(c, e.Type, e.ID, fmt.Sprintf("fs: upload: %v", err))
	}

	return sendOK(c, e.Type, e.ID, uploadResp{Path: abs})
}

// uploadsDir returns the directory uploads are stored in: <root>/.uploads when
// the profile is sandboxed, else ~/.remote-host/uploads for admin profiles.
func uploadsDir(root string) (string, error) {
	if root != "" {
		cleanRoot, err := resolveRoot(root)
		if err != nil {
			return "", err
		}
		return filepath.Join(cleanRoot, ".uploads"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("fs: upload: cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".remote-host", "uploads"), nil
}

// maxUploadAttempts bounds the O_EXCL retry loop in createUploadFile so a
// pathological collision (or a dir full of matching names) fails loudly instead
// of spinning forever.
const maxUploadAttempts = 100

// createUploadFile writes data to a freshly created, never-pre-existing file in
// dir and returns its absolute path. The name is "<unixnano>-<base>" (and
// "<unixnano>-<n>-<base>" on the n-th collision), where base is the sanitized
// basename of the client-supplied name.
//
// The file is opened with O_CREATE|O_EXCL so two concurrent uploads that pick
// the same timestamp can never clobber each other: the loser gets ErrExist and
// retries with the next counter. A partial write leaves no file behind.
func createUploadFile(dir, name string, data []byte) (string, error) {
	base := sanitizeUploadBase(name)
	ts := time.Now().UnixNano()
	for attempt := 0; attempt < maxUploadAttempts; attempt++ {
		candidate := fmt.Sprintf("%d-%s", ts, base)
		if attempt > 0 {
			candidate = fmt.Sprintf("%d-%d-%s", ts, attempt, base)
		}
		abs := filepath.Join(dir, candidate)

		f, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				continue // name taken by a concurrent upload — try the next one
			}
			return "", fmt.Errorf("create %s: %w", abs, err)
		}

		if _, werr := f.Write(data); werr != nil {
			_ = f.Close()
			_ = os.Remove(abs) // don't leave a half-written file around
			return "", fmt.Errorf("write %s: %w", abs, werr)
		}
		if cerr := f.Close(); cerr != nil {
			_ = os.Remove(abs)
			return "", fmt.Errorf("close %s: %w", abs, cerr)
		}
		return abs, nil
	}
	return "", fmt.Errorf("could not allocate a unique name in %s after %d attempts", dir, maxUploadAttempts)
}

// sanitizeUploadBase reduces a client-supplied name to a safe single path
// element: directory components and path separators are stripped (so the result
// always stays inside the uploads dir), and a blank or unsafe name falls back to
// "upload".
func sanitizeUploadBase(name string) string {
	base := filepath.Base(filepath.Clean(strings.ReplaceAll(name, "\\", "/")))
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == ".." || base == string(filepath.Separator) {
		base = "upload"
	}
	return base
}

func (h *Handler) handleMkdir(_ context.Context, e protocol.Envelope, c channel.Conn) error {
	req, err := bindPathReq(e)
	if err != nil {
		return sendErr(c, e.Type, e.ID, err.Error())
	}

	root := c.Profile().Root
	abs, err := resolvePath(req.Path, root)
	if err != nil {
		return sendErr(c, e.Type, e.ID, err.Error())
	}

	if err := os.MkdirAll(abs, 0o755); err != nil {
		return sendErr(c, e.Type, e.ID, fmt.Sprintf("fs: mkdir %s: %v", abs, err))
	}

	return sendOK(c, e.Type, e.ID, okResp{OK: true})
}

func (h *Handler) handleDelete(_ context.Context, e protocol.Envelope, c channel.Conn) error {
	req, err := bindPathReq(e)
	if err != nil {
		return sendErr(c, e.Type, e.ID, err.Error())
	}

	root := c.Profile().Root
	abs, err := resolvePath(req.Path, root)
	if err != nil {
		return sendErr(c, e.Type, e.ID, err.Error())
	}

	// Never RemoveAll the filesystem root or the sandbox root itself: a path of
	// "/" or "." cleans to the root, which passes the boundary check, and
	// os.RemoveAll(root) would wipe the entire tree.
	if abs == string(filepath.Separator) {
		return sendErr(c, e.Type, e.ID, "fs: refusing to delete filesystem root")
	}
	if root != "" {
		if cleanRoot, rerr := resolveRoot(root); rerr == nil && abs == cleanRoot {
			return sendErr(c, e.Type, e.ID, "fs: refusing to delete sandbox root")
		}
	}

	if err := os.RemoveAll(abs); err != nil {
		return sendErr(c, e.Type, e.ID, fmt.Sprintf("fs: delete %s: %v", abs, err))
	}

	return sendOK(c, e.Type, e.ID, okResp{OK: true})
}
