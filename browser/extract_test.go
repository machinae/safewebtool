package browser

import (
	"strings"
	"testing"
)

func TestToMarkdownMixedContent(t *testing.T) {
	html := `<h2>Training</h2><p>The model was <strong>trained</strong> on a <a href="https://example.com">large dataset</a>.</p><ul><li>Item one</li><li>Item two</li></ul>`
	md, err := toMarkdown(html)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Training", "**trained**", "[large dataset]", "Item one", "Item two"} {
		if !strings.Contains(md, want) {
			t.Errorf("expected %q in output, got %q", want, md)
		}
	}
}

func TestToMarkdownDropsDataImages(t *testing.T) {
	md, err := toMarkdown(`<p>Logo <img src="data:image/png;base64,iVBORw0KGgo" alt="x"> <img src="https://example.com/a.png" alt="photo"></p>`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(md, "data:") {
		t.Errorf("data: image not dropped: %q", md)
	}
	if !strings.Contains(md, "![photo](https://example.com/a.png)") {
		t.Errorf("expected regular image kept, got %q", md)
	}
}

func TestToMarkdownEmptyInput(t *testing.T) {
	md, err := toMarkdown("")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(md) != "" {
		t.Errorf("expected empty output, got %q", md)
	}
}
