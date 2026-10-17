package candidate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// VersionReceipt records a native CI invocation, bound to exact binary bytes.
// It is provenance supplied by the build owner, not a publisher signature and
// not independent evidence that the target OS was qualified.
type VersionReceipt struct {
	Format       int    `json:"format"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Command      string `json:"command"`
	Version      string `json:"version"`
	BinarySHA256 string `json:"binary_sha256"`
	Schema       int64  `json:"schema,omitempty"`
}

func verifyReceipt(ctx context.Context, source string, in input, platform, command, version string, schema int64) (err error) {
	f, err := regular(source + ".version.json")
	if err != nil {
		return fmt.Errorf("native version receipt unavailable for %s %s", platform, command)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	body, err := io.ReadAll(io.LimitReader(contextReader{ctx, f}, 4097))
	if err != nil {
		return err
	}
	if len(body) > 4096 {
		return errors.New("native version receipt exceeds bounds")
	}
	scan := json.NewDecoder(bytes.NewReader(body))
	if err = scanJSON(scan, 0); err != nil {
		return errors.New("invalid or duplicate native version receipt JSON")
	}
	if _, err = scan.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing receipt JSON")
	}
	var r VersionReceipt
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err = d.Decode(&r); err != nil {
		return errors.New("invalid native version receipt")
	}
	if r.Format != 1 || r.OS != platform || r.Arch != "amd64" || r.Command != command || r.Version != version || r.BinarySHA256 != in.SHA256 || r.Schema != schema {
		return fmt.Errorf("native version receipt does not match %s %s bytes/version/schema", platform, command)
	}
	return nil
}
func scanJSON(d *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("JSON depth")
	}
	t, e := d.Token()
	if e != nil {
		return e
	}
	v, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch v {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, e := d.Token()
			if e != nil {
				return e
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return errors.New("duplicate JSON member")
			}
			seen[key] = true
			if e = scanJSON(d, depth+1); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e = scanJSON(d, depth+1); e != nil {
				return e
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, e = d.Token()
	return e
}
