package filesystem

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

var ErrOutsideRoot = errors.New("path is outside configured root")
var ErrNotText = errors.New("file does not appear to be text")

type Entry struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Size     int64   `json:"size,omitempty"`
	Children []Entry `json:"children"`
}

type Root struct {
	Path        string
	Ignore      []string
	MaxFileSize int64
}

func New(path string, ignore []string, max int64) (*Root, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("root must be a directory")
	}
	return &Root{Path: filepath.Clean(real), Ignore: ignore, MaxFileSize: max}, nil
}
func (r *Root) ignored(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range r.Ignore {
		p = strings.Trim(filepath.ToSlash(p), "/")
		if p == "" {
			continue
		}
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
		if ok, _ := filepath.Match(p, filepath.Base(rel)); ok {
			return true
		}
	}
	return false
}
func (r *Root) IsIgnored(rel string) bool { return r.ignored(rel) }
func (r *Root) Resolve(rel string) (string, error) {
	rel = filepath.FromSlash(rel)
	if filepath.IsAbs(rel) {
		return "", ErrOutsideRoot
	}
	p := filepath.Clean(filepath.Join(r.Path, rel))
	rr, err := filepath.Rel(r.Path, p)
	if err != nil || rr == ".." || strings.HasPrefix(rr, ".."+string(filepath.Separator)) {
		return "", ErrOutsideRoot
	}
	cur := r.Path
	if rr != "." {
		for _, part := range strings.Split(rr, string(filepath.Separator)) {
			cur = filepath.Join(cur, part)
			info, e := os.Lstat(cur)
			if e != nil {
				return "", e
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, e := filepath.EvalSymlinks(cur)
				if e != nil {
					return "", e
				}
				target, e = filepath.Rel(r.Path, target)
				if e != nil || target == ".." || strings.HasPrefix(target, ".."+string(filepath.Separator)) {
					return "", ErrOutsideRoot
				}
			}
		}
	}
	if r.ignored(rr) {
		return "", fs.ErrNotExist
	}
	return p, nil
}
func (r *Root) Tree() (Entry, error) {
	info, err := os.Stat(r.Path)
	if err != nil {
		return Entry{}, err
	}
	e := Entry{Name: filepath.Base(r.Path), Type: "directory"}
	_ = info
	err = r.addChildren(&e, r.Path)
	return e, err
}
func (r *Root) addChildren(parent *Entry, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) && dir != r.Path {
			return nil
		}
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, d := range entries {
		path := filepath.Join(dir, d.Name())
		rel, _ := filepath.Rel(r.Path, path)
		if r.ignored(rel) {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		child := Entry{Name: d.Name(), Size: info.Size()}
		if info.IsDir() {
			child.Type = "directory"
			if err := r.addChildren(&child, path); err != nil {
				return err
			}
		} else if info.Mode().IsRegular() {
			child.Type = "file"
		} else {
			continue
		}
		parent.Children = append(parent.Children, child)
	}
	return nil
}
func (r *Root) Read(rel string) ([]byte, error) {
	p, err := r.Resolve(rel)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if info.Size() > r.MaxFileSize {
		return nil, errors.New("file exceeds maximum size")
	}
	b, err := io.ReadAll(io.LimitReader(f, r.MaxFileSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > r.MaxFileSize {
		return nil, errors.New("file exceeds maximum size")
	}
	if !utf8.Valid(b) || strings.IndexByte(string(b), 0) >= 0 || (filepath.Ext(p) != "" && !textExtension(filepath.Ext(p))) {
		return nil, ErrNotText
	}
	return b, nil
}

func textExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".html", ".htm", ".css", ".js", ".jsx", ".ts", ".tsx", ".json", ".md", ".mdx", ".astro", ".vue", ".svelte", ".go", ".rs", ".py", ".java", ".cpp", ".c", ".h", ".hpp", ".yaml", ".yml", ".toml", ".xml", ".svg", ".txt", ".mod", ".sum":
		return true
	default:
		return false
	}
}
