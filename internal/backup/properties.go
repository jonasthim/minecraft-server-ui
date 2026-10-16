package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"io"
	"os"
)

func (s *Service) sanitizeProperties(ctx context.Context, stage *source, files []File) ([]File, error) {
	for i, f := range files {
		if f.Path != "server.properties" {
			continue
		}
		in, err := stage.root.Open(f.Path)
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(contextReader{ctx, io.LimitReader(in, (1<<20)+1)})
		err = errors.Join(err, in.Close())
		if err != nil {
			return nil, err
		}
		raw, err = instance.BackupProperties(raw)
		if err != nil {
			return nil, err
		}
		out, err := stage.root.OpenFile(f.Path, os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return nil, err
		}
		_, writeErr := out.Write(raw)
		err = errors.Join(writeErr, out.Sync(), out.Close())
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(raw)
		files[i] = File{f.Path, int64(len(raw)), hex.EncodeToString(hash[:])}
	}
	return files, nil
}
