package main

import (
	"io"
	"os"

	"github.com/goodblaster/parchmint/textlayer"
)

// stdinName is the pseudo-path meaning "read the archive from stdin",
// on every archive-consuming subcommand (find/text/lines/mark/pdf).
const stdinName = "-"

// readArchive returns the archive bytes at path, with "-" meaning
// stdin, and the name to call the archive in messages and output.
func readArchive(path string) (data []byte, name string, err error) {
	if path == stdinName {
		data, err = io.ReadAll(os.Stdin)
		return data, "(stdin)", err
	}
	data, err = os.ReadFile(path)
	return data, path, err
}

// loadLayer parses the embedded text layer of the archive at path
// ("-" = stdin). The container is sniffed from the bytes, never the name.
func loadLayer(path string) (*textlayer.Layer, error) {
	data, name, err := readArchive(path)
	if err != nil {
		return nil, err
	}
	return textlayer.FromBytes(data, name)
}

// spillArchive writes archive bytes to a temporary file so the
// browser-driven commands (mark, pdf) can load a stdin archive over
// file:// — Chrome sniffs file: content types from the EXTENSION, so the
// temp name must carry the right one (an .mht served as .tmp renders as
// plain text). Caller removes the file.
func spillArchive(data []byte) (path string, err error) {
	ext := ".html"
	if textlayer.IsMHT(data) {
		ext = ".mht"
	}
	f, err := os.CreateTemp("", "parch-stdin-*"+ext)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
