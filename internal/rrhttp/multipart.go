package rrhttp

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path"
	"slices"
	"strings"
)

// PHP's UPLOAD_ERR_* codes, as RoadRunner reports them.
const (
	uploadOK        = 0 // UPLOAD_ERR_OK
	uploadNoTmpDir  = 6 // UPLOAD_ERR_NO_TMP_DIR: the temp file couldn't be created
	uploadCantWrite = 7 // UPLOAD_ERR_CANT_WRITE
	uploadExtension = 8 // UPLOAD_ERR_EXTENSION: forbidden or not allowed
)

// Uploads controls where uploaded files go.
type Uploads struct {
	// Dir holds the temp files; empty means the OS temp dir.
	Dir string
	// Forbid and Allow are lower-case extensions with the dot (".php"). A
	// forbidden file, or with Allow set a file not in it, isn't stored and
	// gets UPLOAD_ERR_EXTENSION.
	Forbid, Allow []string
}

// upload is a file of a multipart request, stored in a temp file. It
// marshals to the leaf PHP's PSR7Worker reads.
type upload struct {
	Name    string `json:"name"`
	Mime    string `json:"mime"`
	Size    int64  `json:"size"`
	Error   int    `json:"error"`
	TmpName string `json:"tmpName"`
}

// files collects the uploads of a request.
type files struct {
	keys  []string
	byKey map[string][]*upload
	all   []*upload
	errs  []error // storing problems, reported to PHP as upload errors
}

func (fs *files) add(key string, u *upload) {
	if fs.byKey == nil {
		fs.byKey = make(map[string][]*upload)
	}
	if _, ok := fs.byKey[key]; !ok {
		fs.keys = append(fs.keys, key)
	}
	fs.byKey[key] = append(fs.byKey[key], u)
	fs.all = append(fs.all, u)
}

// tree mounts the files by their field names, as the form tree does.
func (fs *files) tree() (tree, error) {
	t := make(tree, len(fs.keys))
	for _, k := range fs.keys {
		if err := mount(t, splitKey(k), fs.byKey[k], firstFile); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// remove deletes the temp files.
func (fs *files) remove() error {
	var errs []error
	for _, u := range fs.all {
		if u.TmpName == "" {
			continue
		}
		if err := os.Remove(u.TmpName); err != nil && !errors.Is(err, os.ErrNotExist) { //nolint:gosec // G703: kest created this temp file
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// readMultipart reads a multipart/form-data body: value parts become form
// fields, file parts temp files. On an error the temp files are removed.
func readMultipart(body io.Reader, boundary string, opts *Uploads) (*form, *files, error) {
	if boundary == "" {
		return nil, nil, errors.New("no boundary")
	}
	mr := multipart.NewReader(body, boundary)
	f, fs := newForm(), &files{}
	fail := func(err error) (*form, *files, error) {
		_ = fs.remove()
		return nil, nil, err
	}
	for parts := 0; ; parts++ {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return f, fs, nil
		}
		if err != nil {
			return fail(err)
		}
		if parts == maxParts {
			return fail(errors.New("too many parts"))
		}

		name := p.FormName()
		switch {
		case name == "":
		case p.FileName() != "":
			u, err := store(p, opts, fs)
			if err != nil {
				return fail(err)
			}
			fs.add(name, u)
		default:
			value, err := io.ReadAll(p)
			if err != nil {
				return fail(err)
			}
			f.add(name, string(value))
		}
	}
}

// store copies a file part to a temp file. Problems on kest's side become
// upload error codes, as in RoadRunner; an error reading the part (the
// client's side) is returned.
func store(p *multipart.Part, opts *Uploads, fs *files) (*upload, error) {
	u := &upload{Name: p.FileName(), Mime: p.Header.Get("Content-Type")}
	ext := strings.ToLower(path.Ext(u.Name))
	if slices.Contains(opts.Forbid, ext) || (len(opts.Allow) > 0 && !slices.Contains(opts.Allow, ext)) {
		u.Error = uploadExtension
		return u, nil
	}

	tmp, err := os.CreateTemp(opts.Dir, "upload")
	if err != nil {
		u.Error = uploadNoTmpDir
		fs.errs = append(fs.errs, fmt.Errorf("store upload %q: %w", u.Name, err))
		return u, nil
	}
	u.TmpName = tmp.Name()
	w := &trackingWriter{w: tmp}
	u.Size, err = io.Copy(w, p)
	if cerr := tmp.Close(); w.err == nil && cerr != nil {
		w.err = cerr
	}
	switch {
	case w.err != nil:
		u.Error = uploadCantWrite
		fs.errs = append(fs.errs, fmt.Errorf("store upload %q: %w", u.Name, w.err))
	case err != nil:
		_ = os.Remove(u.TmpName)
		return nil, err
	}
	return u, nil
}

// trackingWriter remembers write errors, to tell them from read errors in io.Copy.
type trackingWriter struct {
	w   io.Writer
	err error
}

func (t *trackingWriter) Write(b []byte) (int, error) {
	n, err := t.w.Write(b)
	if err != nil {
		t.err = err
	}
	return n, err
}
