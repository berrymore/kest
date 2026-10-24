package rrhttp

import (
	"errors"
	"io"
	"mime/multipart"
)

// readMultipart reads a multipart/form-data body. Value parts become form
// fields; file parts are skipped (uploads: K-043).
func readMultipart(body io.Reader, boundary string) (*form, error) {
	if boundary == "" {
		return nil, errors.New("no boundary")
	}
	mr := multipart.NewReader(body, boundary)
	f := newForm()
	for parts := 0; ; parts++ {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return f, nil
		}
		if err != nil {
			return nil, err
		}
		if parts == maxParts {
			return nil, errors.New("too many parts")
		}

		name := p.FormName()
		switch {
		case name == "":
		case p.FileName() != "":
		default:
			value, err := io.ReadAll(p)
			if err != nil {
				return nil, err
			}
			f.add(name, string(value))
		}
	}
}
