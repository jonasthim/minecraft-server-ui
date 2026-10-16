package modpack

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"io"
	"os"
)

func invalid(s string) error  { return domain.E(domain.CodeValidationFailed, s) }
func conflict(s string) error { return domain.E(domain.CodeConflict, s) }
func digest(b []byte) string  { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func newID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("secure random unavailable")
	}
	return hex.EncodeToString(b[:])
}
func currentHash(ctx context.Context, r *os.Root, name string, max int64) (string, []byte, error) {
	h, e := content.PackCurrentHash(ctx, r, name, max)
	return h, nil, e
}
func readFile(ctx context.Context, r *os.Root, name string, max int64) ([]byte, error) {
	return content.PackReadFile(ctx, r, name, max)
}
func openChecked(r *os.Root, name string, max int64) (*os.File, error) {
	return content.PackOpenChecked(r, name, max)
}
func atomicStream(ctx context.Context, r *os.Root, name, expected string, reader io.Reader, remove bool) error {
	return content.PackAtomicStream(ctx, r, name, expected, reader, remove)
}
func atomicFile(ctx context.Context, r *os.Root, name, expected string, b []byte, remove bool) error {
	return atomicStream(ctx, r, name, expected, bytes.NewReader(b), remove)
}
func syncDir(r *os.Root) error { return content.PackSyncDirectory(r) }
func (s *Service) openServer(ctx context.Context, id string) (*os.Root, error) {
	d, e := s.paths.For(id)
	if e != nil {
		return nil, e
	}
	return safepath.OpenRoot(ctx, d.Server)
}
func (s *Service) revalidateServer(ctx context.Context, id string, opened *os.Root) error {
	r, e := s.openServer(ctx, id)
	if e != nil {
		return e
	}
	defer r.Close()
	before, e := opened.Stat(".")
	if e != nil {
		return e
	}
	after, e := r.Stat(".")
	if e != nil {
		return e
	}
	if !os.SameFile(before, after) {
		return conflict("canonical server directory changed")
	}
	return nil
}
