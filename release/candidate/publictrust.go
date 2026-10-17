package candidate

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
)

// ParsePublicTrust parses the existing native public-key file format and binds
// it to a repository supplied independently by the caller. Parsing a downloaded
// trust file does not establish production publisher identity.
func ParsePublicTrust(body []byte, repository string) (Trust, error) {
	var result Trust
	if len(body) < 1 || len(body) > 16<<10 {
		return result, errors.New("public trust exceeds bounds")
	}
	scan := json.NewDecoder(bytes.NewReader(body))
	if e := scanJSON(scan, 0); e != nil {
		return result, errors.New("invalid public trust JSON")
	}
	if _, e := scan.Token(); !errors.Is(e, io.EOF) {
		return result, errors.New("trailing public trust JSON")
	}
	var wire struct {
		Format     int               `json:"format"`
		Repository string            `json:"repository"`
		PublicKeys map[string]string `json:"public_keys"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if e := d.Decode(&wire); e != nil {
		return result, errors.New("invalid public trust fields")
	}
	if wire.Format != 1 || wire.Repository != repository || !repositoryRE.MatchString(repository) || bytes.Contains([]byte(repository), []byte("..")) || len(wire.PublicKeys) < 1 || len(wire.PublicKeys) > 16 {
		return result, errors.New("public trust identity or key count is invalid")
	}
	result = Trust{Repository: repository, PublicKeys: map[string]ed25519.PublicKey{}}
	for id, encoded := range wire.PublicKeys {
		key, e := base64.StdEncoding.Strict().DecodeString(encoded)
		if !keyRE.MatchString(id) || e != nil || len(key) != ed25519.PublicKeySize {
			return Trust{}, errors.New("invalid public verification key")
		}
		result.PublicKeys[id] = key
	}
	return result, nil
}
