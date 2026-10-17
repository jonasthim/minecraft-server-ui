package candidate

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"runtime"
)

// ReadSigningKey accepts a supplied raw 32-byte seed / 64-byte private key or
// strict base64 encoding. Errors never contain signing input. It does not
// generate, retain or publish production signing material.
func ReadSigningKey(r io.Reader) (ed25519.PrivateKey, error) {
	b, e := io.ReadAll(io.LimitReader(r, 1025))
	if e != nil || len(b) > 1024 {
		return nil, errors.New("private signing input unavailable or exceeds bound")
	}
	if len(b) != 32 && len(b) != 64 {
		b, e = base64.StdEncoding.Strict().DecodeString(string(bytes.TrimSpace(b)))
		if e != nil {
			return nil, errors.New("invalid private signing input")
		}
	}
	if len(b) == 32 {
		return ed25519.NewKeyFromSeed(b), nil
	}
	if len(b) == 64 {
		key := ed25519.NewKeyFromSeed(b[:32])
		if bytes.Equal(key, b) {
			return key, nil
		}
	}
	return nil, errors.New("invalid private signing input")
}

// OpenSigningKey rejects symlinks and, on Unix, group/world access. Windows
// release owners must provision the file with private ACLs; FileMode does not
// describe Windows ACLs. Pipe input avoids a persisted private key file.
func OpenSigningKey(path string) (key ed25519.PrivateKey, err error) {
	f, e := regular(path)
	if e != nil {
		return nil, errors.New("cannot open private signing input")
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, e := f.Stat()
	if e != nil {
		return nil, errors.New("cannot inspect private signing input")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private signing input must not be accessible to group or others")
	}
	return ReadSigningKey(f)
}
