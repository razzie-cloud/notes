package index

import (
	"bytes"
	"errors"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

type Document struct {
	ID      string    `yaml:"id"`
	Title   string    `yaml:"title"`
	Tags    []string  `yaml:"tags"`
	Created time.Time `yaml:"created"`
	Body    string    `yaml:"-"`
}

func Parse(key string, b []byte) (Document, error) {
	d := Document{Tags: []string{}}
	if !utf8.Valid(b) || bytes.ContainsRune(b, 0) {
		return d, errors.New("Markdown must be UTF-8 text")
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	if strings.HasPrefix(text, "---\n") {
		end := strings.Index(text[4:], "\n---\n")
		closing := 5
		if end < 0 && strings.HasSuffix(text, "\n---") {
			end = len(text) - 4 - 4
			closing = 4
		}
		if end < 0 {
			return d, errors.New("unterminated front matter")
		}
		end += 4
		if end > 64<<10 {
			return d, errors.New("front matter exceeds limit")
		}
		if e := yaml.Unmarshal([]byte(text[4:end]), &d); e != nil {
			return d, errors.New("invalid front matter")
		}
		text = text[end+closing:]
	}
	d.Body = text
	if d.Title == "" {
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "# ") {
				d.Title = strings.TrimSpace(line[2:])
				break
			}
		}
	}
	if d.Title == "" {
		d.Title = strings.TrimSuffix(path.Base(key), ".md")
	}
	if len(d.Title) > 1024 || len(d.ID) > 256 || len(d.Tags) > 100 || strings.ContainsRune(d.Title, 0) || strings.ContainsRune(d.ID, 0) {
		return d, errors.New("note metadata exceeds limits")
	}
	seen := map[string]bool{}
	tags := []string{}
	for _, tag := range d.Tags {
		tag = strings.TrimSpace(tag)
		if len(tag) > 128 || strings.ContainsRune(tag, 0) {
			return d, errors.New("tag exceeds limit")
		}
		if tag != "" && !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	d.Tags = tags
	return d, nil
}
