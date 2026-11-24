package process

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/internal/git/env"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type Blob struct {
	Name, OID string
	Symlink   bool
}

// BlobPayload reads a cat-file payload and its trailing separator before returning.
type BlobPayload struct {
	reader *bufio.Reader
	size   int
}

func (p BlobPayload) Bytes() ([]byte, error) {
	data := make([]byte, p.size+1)
	if _, err := io.ReadFull(p.reader, data); err != nil {
		return nil, err
	}
	return data[:p.size], nil
}

func (p BlobPayload) WriteTo(w io.Writer) (int64, error) {
	n, err := io.CopyN(w, p.reader, int64(p.size))
	if err == nil {
		_, err = p.reader.ReadByte()
	}
	return n, err
}

func (p BlobPayload) Size() int { return p.size }

// ReadBlobs streams one cat-file batch. The callback consumes each payload before returning.
func ReadBlobs(ctx context.Context, root string, blobs []Blob, consume func(Blob, BlobPayload) error) error {
	// One cat-file process avoids spawning Git once for every unchanged file.
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	cmd.Env = env.ForCheckout(os.Environ())
	cmd.Dir = root
	var input strings.Builder
	for _, b := range blobs {
		fmt.Fprintln(&input, b.OID)
	}
	cmd.Stdin = strings.NewReader(input.String())
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	finished := false
	defer func() {
		if !finished {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	reader := bufio.NewReader(pipe)
	for _, b := range blobs {
		header, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read blob %s: %w", b.Name, err)
		}
		parts := strings.Fields(header)
		if len(parts) != 3 || parts[1] != "blob" {
			return fmt.Errorf("invalid blob header for %s", b.Name)
		}
		size, err := strconv.Atoi(parts[2])
		if err != nil || size < 0 {
			return fmt.Errorf("invalid blob size for %s", b.Name)
		}
		if err := consume(b, BlobPayload{reader: reader, size: size}); err != nil {
			return err
		}
	}
	err = cmd.Wait()
	finished = true
	if err != nil {
		return fmt.Errorf("git cat-file: %w: %s", err, stderr.String())
	}
	return nil
}
