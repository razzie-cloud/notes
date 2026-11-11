package app

import (
	"bytes"
	"errors"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/razzie-cloud/notes/internal/index"
	"github.com/razzie-cloud/notes/internal/storage"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"gopkg.in/yaml.v3"
)

type TagCount struct {
	Name  string
	Count int
}

type Attachment struct {
	WriteETag string `json:"-"`
	Key       string `json:"key"`
	Size      int64  `json:"size"`
	ETag      string `json:"etag"`
}

func (a *App) librarySummary(r *http.Request) (int, []TagCount, error) {
	var total int
	err := a.Engine.Pool.QueryRow(r.Context(), `SELECT count(*) FROM objects WHERE scope=$1 AND owner=$2 AND kind='notes'`, a.Engine.Scope, owner(r)).Scan(&total)
	if err != nil {
		return 0, nil, err
	}
	rows, err := a.Engine.Pool.Query(r.Context(), `SELECT tag,count(*) FROM objects CROSS JOIN LATERAL unnest(tags) AS tag WHERE scope=$1 AND owner=$2 AND kind='notes' GROUP BY tag ORDER BY count(*) DESC,tag LIMIT 200`, a.Engine.Scope, owner(r))
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	tags := []TagCount{}
	for rows.Next() {
		var tag TagCount
		if err := rows.Scan(&tag.Name, &tag.Count); err != nil {
			return 0, nil, err
		}
		tags = append(tags, tag)
	}
	return total, tags, rows.Err()
}

func (a *App) attachmentList(r *http.Request, note string) ([]Attachment, error) {
	rows, err := a.Engine.Pool.Query(r.Context(), `SELECT relative_key,size,etag FROM objects WHERE scope=$1 AND owner=$2 AND kind='attachments' AND note_key=$3 ORDER BY key LIMIT 100`, a.Engine.Scope, owner(r), note)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Attachment{}
	for rows.Next() {
		var item Attachment
		if err := rows.Scan(&item.Key, &item.Size, &item.ETag); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *App) tagsPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, View{TagsPage: true}, http.StatusOK)
}

func (a *App) readPage(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	full := a.Engine.Prefix + owner(r) + "/notes/" + key
	if _, _, _, _, ok := storage.Split(a.Engine.Prefix, full); !ok {
		a.fail(w, r, storage.ErrPath)
		return
	}
	body, meta, err := a.Engine.Storage.Get(r.Context(), full, "", a.Config.MaxNoteBytes)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	doc, err := index.Parse(key, body)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	attachments, err := a.attachmentList(r, key)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	v := View{Reading: true, Key: key, Title: doc.Title, NoteTags: doc.Tags, TagsText: strings.Join(doc.Tags, ", "), Modified: meta.Modified, Attachments: attachments, Rendered: renderMarkdown(doc.Body, a.Notebook.ID, key)}
	a.render(w, r, v, http.StatusOK)
}

func (a *App) preview(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, a.Config.MaxNoteBytes*4+4096)
	if r.ParseForm() != nil || int64(len(r.PostForm.Get("content"))) > a.Config.MaxNoteBytes {
		a.fail(w, r, storage.ErrTooLarge)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(renderMarkdown(r.PostForm.Get("content"), a.Notebook.ID, r.PostForm.Get("key"))))
}

func (a *App) deleteAttachmentForm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if r.ParseForm() != nil {
		a.fail(w, r, storage.ErrPath)
		return
	}
	note, relative, etag := r.PostForm.Get("note"), r.PostForm.Get("key"), r.PostForm.Get("etag")
	full := a.Engine.Prefix + owner(r) + "/attachments/" + relative
	_, kind, _, parent, ok := storage.Split(a.Engine.Prefix, full)
	if !ok || kind != "attachments" || parent != note {
		a.fail(w, r, storage.ErrPath)
		return
	}
	if !validETag(etag) {
		http.Error(w, "Current ETag required", 428)
		return
	}
	if err := a.delete(r.Context(), full, etag); err != nil {
		a.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/edit?notebook="+url.QueryEscape(a.Notebook.ID)+"&key="+url.QueryEscape(note), http.StatusSeeOther)
}

func splitFrontMatter(raw string) (string, string) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(raw, "---\n") {
		return "", raw
	}
	if end := strings.Index(raw[4:], "\n---\n"); end >= 0 {
		return raw[4 : 4+end], raw[4+end+5:]
	}
	if strings.HasSuffix(raw, "\n---") {
		return raw[4 : len(raw)-4], ""
	}
	return "", raw
}

func editorFields(v *View) {
	if v.ETag == "*" && v.Content == "" {
		return
	}
	v.FrontMatter, v.Body = splitFrontMatter(v.Content)
	if doc, err := index.Parse(v.Key, []byte(v.Content)); err == nil {
		v.Title, v.TagsText = doc.Title, strings.Join(doc.Tags, ", ")
	}
}

// Merge editable metadata into the original YAML tree, retaining unknown fields,
// stable IDs, timestamps and comments from imported Markdown.
func composeMarkdown(front, body, title, tags string) (string, error) {
	if len(front) > 64<<10 {
		return "", errors.New("front matter exceeds limit")
	}
	var doc yaml.Node
	if strings.TrimSpace(front) != "" {
		if err := yaml.Unmarshal([]byte(front), &doc); err != nil {
			return "", err
		}
	}
	if len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	mapping := doc.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return "", errors.New("front matter must be a mapping")
	}
	set := func(key string, value any) error {
		var node yaml.Node
		if err := node.Encode(value); err != nil {
			return err
		}
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value == key {
				mapping.Content[i+1] = &node
				return nil
			}
		}
		mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &node)
		return nil
	}
	if err := set("title", strings.TrimSpace(title)); err != nil {
		return "", err
	}
	list := []string{}
	for _, tag := range strings.Split(tags, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			list = append(list, tag)
		}
	}
	if err := set("tags", list); err != nil {
		return "", err
	}
	encoded, err := yaml.Marshal(&doc)
	return "---\n" + string(encoded) + "---\n" + body, err
}

// Goldmark escapes text and omits raw HTML by default. Only this renderer may
// produce trusted HTML; Markdown images never emit image elements or requests.
func renderMarkdown(body, notebook, key string) template.HTML {
	md := goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.TaskList), goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(markdownLinks{notebook, key}, 100))))
	var out bytes.Buffer
	if err := md.Convert([]byte(body), &out); err != nil {
		return template.HTML(html.EscapeString(body))
	}
	return template.HTML(out.String())
}

func markdownExcerpt(body string) string {
	source := []byte(body)
	doc := goldmark.New().Parser().Parse(text.NewReader(source))
	var out strings.Builder
	ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if node.Type() == ast.TypeBlock {
			out.WriteByte(' ')
		}
		if value, ok := node.(*ast.Text); ok {
			out.Write(value.Segment.Value(source))
			if value.SoftLineBreak() || value.HardLineBreak() {
				out.WriteByte(' ')
			}
		}
		if value, ok := node.(*ast.String); ok {
			out.Write(value.Value)
		}
		return ast.WalkContinue, nil
	})
	return strings.Join(strings.Fields(out.String()), " ")
}

type markdownLinks struct{ notebook, key string }

func (m markdownLinks) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindHeading, func(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
		level := strconv.Itoa(max(2, node.(*ast.Heading).Level))
		if entering {
			_, err := w.WriteString("<h" + level + ">")
			return ast.WalkContinue, err
		}
		_, err := w.WriteString("</h" + level + ">\n")
		return ast.WalkContinue, err
	})
	reg.Register(ast.KindAutoLink, func(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		link := node.(*ast.AutoLink)
		destination := string(link.URL(source))
		if link.AutoLinkType == ast.AutoLinkEmail {
			destination = "mailto:" + strings.TrimPrefix(destination, "mailto:")
		}
		_, err := w.WriteString(`<a rel="noreferrer noopener" href="` + html.EscapeString(m.href(destination)) + `">` + html.EscapeString(string(link.Label(source))) + `</a>`)
		return ast.WalkSkipChildren, err
	})
	reg.Register(ast.KindImage, func(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			_, err := w.WriteString(html.EscapeString(string(node.Text(source))))
			return ast.WalkSkipChildren, err
		}
		return ast.WalkContinue, nil
	})
	reg.Register(ast.KindLink, func(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			_, err := w.WriteString(`<a rel="noreferrer noopener" href="` + html.EscapeString(m.href(string(node.(*ast.Link).Destination))) + `">`)
			return ast.WalkContinue, err
		}
		_, err := w.WriteString("</a>")
		return ast.WalkContinue, err
	})
}

func (m markdownLinks) href(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Scheme != "" {
		if u.Scheme == "https" || u.Scheme == "http" || u.Scheme == "mailto" {
			return u.String()
		}
		return ""
	}
	if u.Host != "" || strings.HasPrefix(u.Path, "/") {
		return ""
	}
	if u.Path == "" {
		if u.Fragment != "" {
			return "#" + url.PathEscape(u.Fragment)
		}
		return ""
	}
	if !storage.Path(u.Path) {
		return ""
	}
	if strings.HasSuffix(u.Path, ".md") {
		return "/note?notebook=" + url.QueryEscape(m.notebook) + "&key=" + url.QueryEscape(path.Join(path.Dir(m.key), u.Path))
	}
	return (&url.URL{Path: "/v1/attachments/" + m.key + "/" + u.Path, RawQuery: "notebook=" + url.QueryEscape(m.notebook)}).String()
}
