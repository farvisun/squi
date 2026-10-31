// Command site builds squi's GitHub Pages site: the landing page and the user
// guide rendered from docs/USAGE.md.
//
//	go run . -out _site
package main

import (
	"bytes"
	"embed"
	"flag"
	"fmt"
	"html/template"
	"log"
	"os"
	"path/filepath"
	"strings"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

//go:embed templates
var templates embed.FS

const repo = "https://github.com/farvisun/squi"

type heading struct {
	ID, Text string
	Level    int
}

type page struct {
	Title   string
	Version string
	Body    template.HTML
	TOC     []heading
}

func main() {
	root := flag.String("root", "..", "repository root")
	out := flag.String("out", "_site", "output directory")
	version := flag.String("version", "", "latest release tag, shown on the landing page")
	flag.Parse()
	if err := build(*root, *out, *version); err != nil {
		log.Fatal(err)
	}
}

func build(root, out, version string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	tpl, err := template.ParseFS(templates, "templates/*.html")
	if err != nil {
		return err
	}
	src, err := os.ReadFile(filepath.Join(root, "docs", "USAGE.md"))
	if err != nil {
		return err
	}
	body, toc, err := render(src)
	if err != nil {
		return err
	}
	pages := map[string]page{
		"index.html": {Title: "squi — a keyboard-driven SQL client for your terminal", Version: version},
		"docs.html":  {Title: "User guide — squi", Version: version, Body: body, TOC: toc},
	}
	for name, p := range pages {
		var buf bytes.Buffer
		if err := tpl.ExecuteTemplate(&buf, name, p); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(out, name), buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	css, err := templates.ReadFile("templates/style.css")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "style.css"), css, 0o644); err != nil {
		return err
	}
	for _, f := range []string{"demo.mp4", "preview.png"} {
		b, err := os.ReadFile(filepath.Join(root, "docs", f))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, f), b, 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(out, ".nojekyll"), nil, 0o644)
}

// render turns the guide into HTML and collects its h2/h3 headings. The h1
// is dropped (the page has its own title) and links to repository files point
// at GitHub or the landing page.
func render(src []byte) (template.HTML, []heading, error) {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM, highlighting.NewHighlighting(
			highlighting.WithStyle("catppuccin-mocha"),
			highlighting.WithFormatOptions(chromahtml.WithClasses(false)),
		)),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	doc := md.Parser().Parse(text.NewReader(src))
	var toc []heading
	var drop []ast.Node
	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Heading:
			if n.Level == 1 {
				drop = append(drop, n)
				return ast.WalkSkipChildren, nil
			}
			if n.Level <= 3 {
				id, _ := n.AttributeString("id")
				toc = append(toc, heading{ID: string(id.([]byte)), Text: plain(n, src), Level: n.Level})
			}
		case *ast.Link:
			n.Destination = []byte(link(string(n.Destination)))
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return "", nil, err
	}
	for _, n := range drop {
		n.Parent().RemoveChild(n.Parent(), n)
	}
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		return "", nil, err
	}
	return template.HTML(buf.String()), toc, nil
}

func link(dest string) string {
	switch {
	case dest == "../README.md":
		return "index.html#install"
	case strings.HasPrefix(dest, "../"):
		return repo + "/blob/main/" + strings.TrimPrefix(dest, "../")
	case !strings.Contains(dest, ":") && !strings.HasPrefix(dest, "#"):
		return repo + "/blob/main/docs/" + dest
	}
	return dest
}

func plain(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if t, ok := c.(*ast.Text); ok && entering {
			b.Write(t.Segment.Value(src))
		}
		if t, ok := c.(*ast.String); ok && entering {
			b.Write(t.Value)
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}
