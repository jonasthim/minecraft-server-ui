package console

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

type rawCursor struct {
	paths    instance.Paths
	id       string
	identity os.FileInfo
	offset   int64
	pending  []byte
	discard  bool
}

func newRawCursor(ctx context.Context, paths instance.Paths, id string) (*rawCursor, error) {
	p, err := paths.For(id)
	if err != nil {
		return nil, err
	}
	root, err := safepath.OpenRoot(ctx, p.Logs)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat("console.log")
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, domain.E(domain.CodeUnavailable, "native console cursor is unavailable")
	}
	f, err := root.Open("console.log")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, stat) {
		return nil, domain.E(domain.CodeUnavailable, "native console changed while opening")
	}
	c := &rawCursor{paths: paths, id: id, identity: stat, offset: stat.Size()}
	if stat.Size() > 0 {
		var b [1]byte
		if _, err = f.ReadAt(b[:], stat.Size()-1); err != nil {
			return nil, err
		}
		c.discard = b[0] != '\n'
	}
	return c, nil
}
func (c *rawCursor) poll(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := c.paths.For(c.id)
	if err != nil {
		return nil, err
	}
	root, err := safepath.OpenRoot(ctx, p.Logs)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	found := false
	out := []string{}
	total := 0
	for _, name := range []string{"console.log.2", "console.log.1", "console.log"} {
		info, e := root.Lstat(name)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if !info.Mode().IsRegular() {
			return nil, domain.E(domain.CodeUnavailable, "native console file is unsafe")
		}
		f, e := root.Open(name)
		if e != nil {
			return nil, e
		}
		stat, e := f.Stat()
		if e != nil {
			f.Close()
			return nil, e
		}
		if !os.SameFile(info, stat) {
			f.Close()
			return nil, domain.E(domain.CodeUnavailable, "native console rotated while opening")
		}
		offset := int64(0)
		if !found {
			if !os.SameFile(stat, c.identity) {
				f.Close()
				continue
			}
			found = true
			offset = c.offset
			if stat.Size() < offset {
				f.Close()
				return nil, domain.E(domain.CodeUnavailable, "native console acknowledgement cursor was truncated")
			}
		}
		if stat.Size() > 4<<20 || stat.Size()-offset > 1<<20 || total+int(stat.Size()-offset) > 1<<20 {
			f.Close()
			return nil, domain.E(domain.CodeUnavailable, "native console acknowledgement limit exceeded")
		}
		data, e := io.ReadAll(io.NewSectionReader(f, offset, stat.Size()-offset))
		f.Close()
		if e != nil {
			return nil, e
		}
		total += len(data)
		for _, b := range data {
			if b == '\n' {
				if !c.discard {
					out = append(out, string(bytes.TrimSuffix(c.pending, []byte{'\r'})))
				}
				c.pending = nil
				c.discard = false
			} else if !c.discard {
				if len(c.pending) >= maxLineBytes {
					c.pending = nil
					c.discard = true
				} else {
					c.pending = append(c.pending, b)
				}
			}
		}
		c.identity = stat
		c.offset = stat.Size()
	}
	if !found {
		return nil, domain.E(domain.CodeUnavailable, "native console acknowledgement cursor was lost during rotation")
	}
	return out, nil
}

type acknowledgement func(string) bool

func (c *rawCursor) wait(ctx context.Context, oracle acknowledgement) (string, error) {
	timer := time.NewTicker(20 * time.Millisecond)
	defer timer.Stop()
	for {
		lines, err := c.poll(ctx)
		if err != nil {
			return "", err
		}
		for _, line := range lines {
			message, ok := nativeMessage(line)
			if ok && oracle(message) {
				return message, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", domain.E(domain.CodeUnavailable, "server command acknowledgement was not observed before the deadline")
		case <-timer.C:
		}
	}
}

// Only command feedback attributed to the server thread is an acknowledgement.
// Chat/player lines, historical Done and arbitrary plugin-prefixed text fail.
var nativePrefix = regexp.MustCompile(`^\[[0-9]{2}:[0-9]{2}:[0-9]{2}\] \[Server thread/INFO\]: (.*)$`)

func nativeMessage(line string) (string, bool) {
	m := nativePrefix.FindStringSubmatch(line)
	if len(m) != 2 {
		return "", false
	}
	return m[1], true
}

var modernList = regexp.MustCompile(`^There are [0-9]+ of a max of [0-9]+ players online: (?:[A-Za-z0-9_]{1,16}(?:, [A-Za-z0-9_]{1,16})*)?$`)
var oldList = regexp.MustCompile(`^There are [0-9]+/[0-9]+ players online:$`)

func ackList(version string) acknowledgement {
	return func(message string) bool {
		if strings.HasPrefix(version, "1.12.") {
			return oldList.MatchString(message)
		}
		return modernList.MatchString(message)
	}
}
func ackFlush(version string) acknowledgement {
	flushed := false
	saved := false
	return func(message string) bool {
		if strings.HasPrefix(version, "1.12.") {
			if message == "Flushing completed" {
				flushed = true
			}
			if message == "Saved the world" {
				saved = true
			}
			return flushed && saved
		}
		return message == "Saved the game"
	}
}
func ackSaveOn(version string) acknowledgement {
	return func(message string) bool {
		if strings.HasPrefix(version, "1.12.") {
			return message == "Turned on world auto-saving"
		}
		return message == "Automatic saving is now enabled" || message == "Saving is already turned on"
	}
}

var seedReply = regexp.MustCompile(`^Seed: (?:\[)?-?[0-9]+(?:\])?$`)

func ackCommand(selection domain.VersionSelection, command PublicCommand) acknowledgement {
	switch command.Verb {
	case "list":
		return ackList(selection.MinecraftVersion)
	case "say":
		return func(message string) bool { return message == "[Server] "+strings.TrimPrefix(command.Text, "say ") }
	case "seed":
		return seedReply.MatchString
	}
	return func(string) bool { return false }
}
func commandTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 10*time.Second)
}
func wrapNativeError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("native console operation: %w", err)
}
