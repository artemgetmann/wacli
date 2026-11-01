package linkpreview

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// Malformed remote pages can split one text node across many ignored end tags,
// including table foster-parenting. Exercise the real preview fetch/parser
// without timing or process-wide allocation assertions. The fixed dependency
// owns the complexity guarantee; this guards our metadata behavior on the input.
func TestFetchHostileTextCoalescingPreservesMetadata(t *testing.T) {
	for _, table := range []bool{false, true} {
		name, prefix := "body", ""
		if table {
			name, prefix = "foster-parenting", "<table>"
		}
		t.Run(name, func(t *testing.T) {
			page := `<html><head><title>Preserved title</title><meta name="description" content="Preserved description"></head><body>` + prefix + strings.Repeat("a</x>", 10000)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte(page))
			}))
			t.Cleanup(srv.Close)
			got, err := Fetch(t.Context(), srv.Client(), srv.URL)
			if err != nil || got == nil || got.Title != "Preserved title" || got.Description != "Preserved description" {
				t.Fatalf("hostile page metadata = %+v, %v", got, err)
			}
		})
	}
}

// GO-2026-6866: namespace transitions at any ancestor must prevent raw-text
// rendering from turning inert text into an image tag. These are minimized
// dependency regression inputs, not a claim that wacli renders remote HTML.
func TestHTMLRendererEscapesRawTextAcrossForeignAncestors(t *testing.T) {
	for _, input := range []string{
		`<math><mtext><table><mglyph><svg><desc><style><img src=x onerror=alert(1)>`,
		`<math><mtext><table><malignmark><svg><desc><style><img src=x onerror=alert(1)>`,
	} {
		doc, err := html.Parse(strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		var rendered bytes.Buffer
		if err := html.Render(&rendered, doc); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(rendered.String(), "<img") || !strings.Contains(rendered.String(), "&lt;img src=x onerror=alert(1)&gt;") {
			t.Fatalf("foreign ancestor permitted raw tag rendering: %s", rendered.String())
		}
	}
}
