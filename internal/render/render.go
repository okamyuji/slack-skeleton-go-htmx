// Package render html/templateを束ねた薄いレンダラを提供します。
// パッケージレベルでテンプレを解析しておき、リクエストごとの再パースを避けます。
package render

import (
	"embed"
	"fmt"
	"html/template"
	"io"
)

//go:embed templates/*.html templates/partials/*.html
var templatesFS embed.FS

// Renderer 解析済みのテンプレ集合をラップします。
type Renderer struct {
	tmpl *template.Template
}

// New templatesディレクトリを解析したRendererを返します。
func New() (*Renderer, error) {
	tmpl, err := template.New("").ParseFS(templatesFS,
		"templates/*.html",
		"templates/partials/*.html",
	)
	if err != nil {
		return nil, fmt.Errorf("render: parse: %w", err)
	}
	return &Renderer{tmpl: tmpl}, nil
}

// Render 指定したテンプレに値を流し込みます。
func (r *Renderer) Render(w io.Writer, name string, data any) error {
	if err := r.tmpl.ExecuteTemplate(w, name, data); err != nil {
		return fmt.Errorf("render %s: %w", name, err)
	}
	return nil
}
