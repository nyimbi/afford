// Package uploads resolves a file fixture for a file input.
//
// A file input is not "unsupported" any more: the tool attaches a real file so
// the change handler, the client validation and the submit path all run. When
// no fixture is configured, a small valid file matching the input's accept
// attribute is synthesized, so the path is still exercised.
package uploads

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"afford/internal/config"
	"afford/internal/model"
)

type File struct {
	Name     string
	MimeType string
	Buffer   []byte
}

var samplePNG = func() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for x := 0; x < 2; x++ {
		for y := 0; y < 2; y++ {
			img.Set(x, y, color.RGBA{R: 0x33, G: 0x66, B: 0xcc, A: 0xff})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}()

const samplePDF = `%PDF-1.4
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]>>endobj
trailer<</Root 1 0 R>>
%%EOF
`

// Resolve returns the file to attach, or an error naming the missing fixture.
func Resolve(el model.Element, cfg config.UploadsConfig) (File, error) {
	if el.Type != "file" {
		return File{}, errors.New("not a file input")
	}
	if name, ok := cfg.ByName[el.NameAttr]; ok && name != "" {
		return load(name, cfg)
	}
	if name, ok := cfg.ByName[el.ID]; ok && name != "" {
		return load(name, cfg)
	}
	for _, token := range acceptTokens(el) {
		if name, ok := cfg.ByAccept[token]; ok && name != "" {
			return load(name, cfg)
		}
	}
	if cfg.Default != "" {
		return load(cfg.Default, cfg)
	}
	return synthesize(el), nil
}

func acceptTokens(el model.Element) []string {
	raw := el.Attrs["accept"]
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(strings.ToLower(part))
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func load(name string, cfg config.UploadsConfig) (File, error) {
	path := name
	if !filepath.IsAbs(path) && cfg.Dir != "" {
		path = filepath.Join(cfg.Dir, path)
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("upload fixture %q: %w", path, err)
	}
	return File{
		Name:     filepath.Base(name),
		MimeType: mimeFor(name),
		Buffer:   blob,
	}, nil
}

func synthesize(el model.Element) File {
	accept := strings.Join(acceptTokens(el), " ")
	nameHint := strings.ToLower(el.NameAttr + " " + el.Name)
	switch {
	case strings.Contains(accept, "image") || strings.Contains(nameHint, "photo") || strings.Contains(nameHint, "image"):
		return File{Name: "afford-sample.png", MimeType: "image/png", Buffer: samplePNG}
	case strings.Contains(accept, "pdf"):
		return File{Name: "afford-sample.pdf", MimeType: "application/pdf", Buffer: []byte(samplePDF)}
	case strings.Contains(accept, "csv"):
		return File{Name: "afford-sample.csv", MimeType: "text/csv", Buffer: []byte("name,value\nafford,1\n")}
	case strings.Contains(accept, "json"):
		return File{Name: "afford-sample.json", MimeType: "application/json", Buffer: []byte(`{"afford":true}`)}
	}
	return File{Name: "afford-sample.txt", MimeType: "text/plain", Buffer: []byte("afford upload fixture\n")}
}

func mimeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".pdf":
		return "application/pdf"
	case ".csv":
		return "text/csv"
	case ".json":
		return "application/json"
	}
	return "text/plain"
}
