package main

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
)

// ErrTooLarge means a body, once decoded, is longer than the rule allows.
var ErrTooLarge = errors.New("body too large")

// decodeBody reads r, undoes the HTTP Content-Encoding and returns the
// decoded bytes. It fails with ErrTooLarge as soon as the decoded body would
// exceed max bytes, so a compressed bomb is never fully expanded. An encoding
// it does not know is an error; the caller keeps the original bytes for the
// browser either way.
func decodeBody(encoding string, r io.Reader, max int64) ([]byte, error) {
	var codings []string
	for _, c := range strings.Split(encoding, ",") {
		c = strings.ToLower(strings.TrimSpace(c))
		if c != "" && c != "identity" {
			codings = append(codings, c)
		}
	}

	// Encodings are listed in the order applied, so undo them in reverse.
	for i := len(codings) - 1; i >= 0; i-- {
		next, err := wrapDecoder(codings[i], r)
		if err != nil {
			return nil, err
		}
		r = next
	}

	out, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > max {
		return nil, ErrTooLarge
	}
	return out, nil
}

func wrapDecoder(coding string, r io.Reader) (io.Reader, error) {
	switch coding {
	case "gzip", "x-gzip":
		return gzip.NewReader(r)
	case "br":
		return brotli.NewReader(r), nil
	case "deflate":
		// HTTP deflate is zlib-wrapped, but some servers send raw deflate.
		br := bufio.NewReader(r)
		head, err := br.Peek(2)
		if err == nil && head[0]&0x0f == 8 && (uint16(head[0])<<8|uint16(head[1]))%31 == 0 {
			return zlib.NewReader(br)
		}
		return flate.NewReader(br), nil
	default:
		return nil, fmt.Errorf("unsupported content encoding %q", coding)
	}
}
