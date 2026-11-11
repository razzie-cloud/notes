package app

import (
	"strings"
	"testing"

	"github.com/razzie-cloud/notes/internal/index"
)

func TestMarkdownPreviewSafety(t *testing.T) {
	body := "# A heading\n\n**Bold** and `code`.\n\n- [x] Done\n- [ ] Later\n\n> A quotation\n\n```go\n<script>code</script>\n```\n\n<script>alert('raw')</script>\n\n<img src=x onerror=alert(1)>\n\n![Remote image](https://tracker.invalid/pixel)\n\n<javascript:alert%281%29> <https://example.com> <person@example.com> [unsafe](javascript:alert%281%29) [encoded](java&#x73;cript:alert%281%29) [data](data:text/html,test) [relative](//tracker.invalid) [settings](/storage) [file](report.csv) [note](other.md)\n"
	rendered := string(renderMarkdown(body, "book-id", "folder/current.md"))
	for _, want := range []string{"<h2>A heading</h2>", "<strong>Bold</strong>", "<code>code</code>", `type="checkbox"`, "<blockquote>", "&lt;script&gt;code&lt;/script&gt;", "Remote image", "/v1/attachments/folder/current.md/report.csv?notebook=book-id", "/note?notebook=book-id&amp;key=folder%2Fother.md"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in %s", want, rendered)
		}
	}
	for _, bad := range []string{"<script>", "<img", "onerror=", "tracker.invalid", `href="javascript:`, "data:text", `href="/storage"`} {
		if strings.Contains(rendered, bad) {
			t.Errorf("unsafe output %q in %s", bad, rendered)
		}
	}
}

func TestEditorMetadataRoundTrip(t *testing.T) {
	original := "---\nid: portable-id\ntitle: Before\ntags: [old]\ncreated: 2026-01-02T03:04:05Z\ncustom:\n  nested: [keep, me]\n---\n# Body\n"
	v := View{Key: "import.md", Content: original}
	editorFields(&v)
	if v.Title != "Before" || v.TagsText != "old" || v.Body != "# Body\n" {
		t.Fatal(v)
	}
	raw, err := composeMarkdown(v.FrontMatter, "## Changed body\n", "After", "homelab, magyar, homelab")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := index.Parse("import.md", []byte(raw))
	if err != nil || doc.ID != "portable-id" || doc.Title != "After" || strings.Join(doc.Tags, ",") != "homelab,magyar" || doc.Body != "## Changed body\n" || doc.Created.Year() != 2026 || !strings.Contains(raw, "nested:") || !strings.Contains(raw, "keep") {
		t.Fatal(doc, raw, err)
	}
	for _, input := range []string{"# Plain Markdown\n", "---\ntitle: End\n---", "---\ntitle: CRLF\r\n---\r\nbody"} {
		front, body := splitFrontMatter(input)
		rebuilt, err := composeMarkdown(front, body, "Title", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = index.Parse("test.md", []byte(rebuilt)); err != nil {
			t.Fatal(rebuilt, err)
		}
	}
	if _, err = composeMarkdown("[not, a, mapping]", "body", "title", ""); err == nil {
		t.Fatal("non-mapping front matter accepted")
	}
}
