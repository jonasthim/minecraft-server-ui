package modpack

import (
	"bytes"
	"context"
	"io"
	"strings"
	"unicode/utf8"
)

func textDestination(name string) bool {
	return !strings.HasPrefix(name, "mods/") && !binaryResource(name)
}
func validateDestinationSize(name string, size int64) error {
	if !allowedPath(name) || size < 0 || size > MaxFileBytes {
		return invalid("invalid pack member destination or size")
	}
	if textDestination(name) && size > MaxConfigBytes {
		return invalid("pack configuration exceeds 1 MiB text bound")
	}
	return nil
}

// All acquisition paths use the same destination policy. Only approved JAR and
// binary resource destinations can contain arbitrary bytes; text reads stay bounded.
func validateDestination(ctx context.Context, name string, r io.ReaderAt, size int64) error {
	if e := validateDestinationSize(name, size); e != nil {
		return e
	}
	if !textDestination(name) {
		return nil
	}
	raw, e := io.ReadAll(contextReader{ctx, io.NewSectionReader(r, 0, size)})
	if e != nil {
		return e
	}
	if int64(len(raw)) != size || !utf8.Valid(raw) || bytes.ContainsRune(raw, 0) {
		return invalid("pack member is not bounded UTF-8 config text")
	}
	return nil
}
func (s *Service) validateDestinationStage(ctx context.Context, id, name string, st stage) error {
	if e := validateDestinationSize(name, st.Size); e != nil {
		return e
	}
	if !textDestination(name) {
		return nil
	}
	f, e := s.openStage(ctx, id, st)
	if e != nil {
		return e
	}
	defer f.Close()
	return validateDestination(ctx, name, f, st.Size)
}
