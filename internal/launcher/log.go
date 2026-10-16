package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const LogMaxBytes = 4 << 20
const LogFiles = 3

// rollingLog bounds retained raw console output to three files of 4 MiB each.
type rollingLog struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func openLog(dir string) (*rollingLog, error) {
	l := &rollingLog{path: filepath.Join(dir, "console.log")}
	f, err := openPrivate(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	l.f = f
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	l.size = fi.Size()
	return l, nil
}
func (l *rollingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	total := 0
	for len(p) > 0 {
		if l.size >= LogMaxBytes {
			if err := l.rotate(); err != nil {
				return total, err
			}
		}
		n := min(len(p), int(LogMaxBytes-l.size))
		w, err := l.f.Write(p[:n])
		total += w
		l.size += int64(w)
		p = p[w:]
		if err != nil {
			return total, err
		}
		if w == 0 {
			return total, fmt.Errorf("short log write")
		}
	}
	return total, nil
}
func (l *rollingLog) rotate() error {
	if err := l.f.Close(); err != nil {
		return err
	}
	for i := LogFiles - 1; i > 0; i-- {
		src := l.path
		if i > 1 {
			src = fmt.Sprintf("%s.%d", l.path, i-1)
		}
		dst := fmt.Sprintf("%s.%d", l.path, i)
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	f, err := openPrivate(l.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	l.f = f
	l.size = 0
	return nil
}
func (l *rollingLog) Close() error { l.mu.Lock(); defer l.mu.Unlock(); return l.f.Close() }
