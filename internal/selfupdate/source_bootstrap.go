package selfupdate

import (
	"context"
	"errors"
	"io"
)

// BootstrapMetadata uses a separate allowlist from the shipping update format.
func (s *Source) BootstrapMetadata(ctx context.Context, tag string) ([]byte, []byte, error) {
	body, err := s.readBootstrap(ctx, tag, "bootstrap.json", 32<<10)
	if err != nil {
		return nil, nil, err
	}
	sig, err := s.readBootstrap(ctx, tag, "bootstrap.sig", 1024)
	return body, sig, err
}

// BootstrapArchive accepts only the two exact native amd64 archive identities.
func (s *Source) BootstrapArchive(ctx context.Context, tag, platform string) (io.ReadCloser, error) {
	name := ""
	switch platform {
	case "linux":
		name = "minecraft-ui-bootstrap-linux-amd64.tar.gz"
	case "windows":
		name = "minecraft-ui-bootstrap-windows-amd64.zip"
	default:
		return nil, errors.New("bootstrap platform is not permitted")
	}
	return s.openBootstrap(ctx, tag, name)
}

func (s *Source) openBootstrap(ctx context.Context, tag, name string) (io.ReadCloser, error) {
	if _, err := ParseStable(tag); err != nil {
		return nil, err
	}
	switch name {
	case "bootstrap.json", "bootstrap.sig", "minecraft-ui-bootstrap-linux-amd64.tar.gz", "minecraft-ui-bootstrap-windows-amd64.zip":
	default:
		return nil, errors.New("bootstrap asset name is not permitted")
	}
	return s.openAsset(ctx, tag, name)
}

func (s *Source) readBootstrap(ctx context.Context, tag, name string, limit int64) ([]byte, error) {
	r, err := s.openBootstrap(ctx, tag, name)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, errors.New("bootstrap response could not be read")
	}
	if int64(len(body)) > limit {
		return nil, errors.New("bootstrap response exceeds size limit")
	}
	return body, nil
}
