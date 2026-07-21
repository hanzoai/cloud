// ui_templates.go — the Hanzo Git UI's view layer: data shapes, the render()
// helper, and the html/template set (chrome + pages). Kept apart from ui.go so
// the handlers read as flow and the markup lives in one place. All dynamic
// values pass through html/template auto-escaping — the XSS boundary.
package git

import (
	"bytes"
	"html/template"
	"net/http"

	"github.com/zap-proto/zip"
)

// ---- view data ----

type repoRow struct {
	Name, Description, DefaultBranch, Size, Updated string
}

type entry struct {
	Name  string
	Href  string
	IsDir bool
}

type commitRow struct {
	Short, Message, Author, When string
}

type crumb struct {
	Name, Href string
}

type homeData struct {
	Base  string
	Org   string
	Repos []repoRow
}

type repoData struct {
	Base                        string
	Org, Repo, Description, Ref string
	CloneHTTP, CloneSSH         string
	Branches                    []string
	Entries                     []entry
	Commits                     []commitRow
	Readme                      string
	Empty                       bool
}

type treeData struct {
	Base                 string
	Org, Repo, Ref, Path string
	Crumbs               []crumb
	Entries              []entry
}

type blobData struct {
	Base                       string
	Org, Repo, Ref, Path, Size string
	Crumbs                     []crumb
	Content                    string
	Lines                      int
	Binary                     bool
}

type commitsData struct {
	Base           string
	Org, Repo, Ref string
	Commits        []commitRow
}

// render writes a page: the named body template wrapped in the shared chrome.
// base is the request's UI base path ("" on the git host, "/git" elsewhere); the
// chrome's home link resolves to base or "/" so it points at the right root on
// either host.
func render(c *zip.Ctx, base string, status int, title string, body *template.Template, data any) error {
	var inner bytes.Buffer
	if err := body.Execute(&inner, data); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "render: %v", err)
	}
	home := base
	if home == "" {
		home = "/"
	}
	var page bytes.Buffer
	if err := chromeTmpl.Execute(&page, chromeData{Title: title, Home: home, Body: template.HTML(inner.String())}); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "render: %v", err)
	}
	c.SetHeader("Content-Type", "text/html; charset=utf-8")
	c.SetHeader("Cache-Control", "no-store")
	return c.Bytes(status, page.Bytes())
}

type chromeData struct {
	Title string
	Home  string
	Body  template.HTML
}

// ---- templates ----

var tmpl = template.Must(template.New("chrome").Parse(chromeHTML))

var (
	chromeTmpl  = tmpl
	homeTmpl    = template.Must(template.New("home").Parse(homeHTML))
	repoTmpl    = template.Must(template.New("repo").Parse(repoHTML))
	treeTmpl    = template.Must(template.New("tree").Parse(treeHTML))
	blobTmpl    = template.Must(template.New("blob").Parse(blobHTML))
	commitsTmpl = template.Must(template.New("commits").Parse(commitsHTML))
)

const chromeHTML = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · Hanzo Git</title>
<style>
:root{--bg:#0d1017;--panel:#151a23;--line:#232a36;--fg:#e6e9ef;--dim:#8b94a7;--acc:#6ea8fe;--mono:ui-monospace,SFMono-Regular,Menlo,monospace}
@media(prefers-color-scheme:light){:root{--bg:#fbfcfe;--panel:#fff;--line:#e4e8ef;--fg:#1a1f2b;--dim:#5b6472;--acc:#2f6fed}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}
a{color:var(--acc);text-decoration:none}a:hover{text-decoration:underline}
header{border-bottom:1px solid var(--line);background:var(--panel)}
.wrap{max-width:1080px;margin:0 auto;padding:0 20px}
.bar{display:flex;align-items:center;gap:10px;height:56px}
.brand{font-weight:700;font-size:17px;letter-spacing:-.01em;color:var(--fg)}
.brand b{color:var(--acc)}
main{max-width:1080px;margin:24px auto;padding:0 20px}
.panel{background:var(--panel);border:1px solid var(--line);border-radius:10px;overflow:hidden}
.row{display:flex;align-items:center;gap:10px;padding:12px 16px;border-top:1px solid var(--line)}
.row:first-child{border-top:0}
.muted{color:var(--dim);font-size:13px}
.mono{font-family:var(--mono)}
h1{font-size:22px;margin:0 0 4px;letter-spacing:-.02em}
h2{font-size:15px;margin:22px 0 10px;color:var(--dim);font-weight:600;text-transform:uppercase;letter-spacing:.04em}
.pill{display:inline-block;padding:2px 9px;border:1px solid var(--line);border-radius:20px;font-size:12px;color:var(--dim)}
.crumbs{margin:0 0 14px;font-family:var(--mono);font-size:14px}
.clone{display:flex;gap:8px;margin:10px 0}
.clone input{flex:1;background:var(--bg);border:1px solid var(--line);border-radius:7px;color:var(--fg);font-family:var(--mono);font-size:13px;padding:8px 10px}
pre{margin:0;padding:14px 16px;overflow:auto;font-family:var(--mono);font-size:13px;line-height:1.55;background:var(--panel)}
.readme{padding:18px;border:1px solid var(--line);border-radius:10px;background:var(--panel);margin-top:18px;white-space:pre-wrap;font-family:var(--mono);font-size:13px}
.ico{width:16px;text-align:center;color:var(--dim)}
footer{max-width:1080px;margin:40px auto;padding:0 20px;color:var(--dim);font-size:13px}
.empty{padding:40px 16px;text-align:center;color:var(--dim)}
</style></head><body>
<header><div class="wrap"><div class="bar">
<a class="brand" href="{{.Home}}"><b>Hanzo</b> Git</a>
</div></div></header>
<main>{{.Body}}</main>
<footer>Hanzo Git — native git hosting on Hanzo IAM. <span class="mono">git.hanzo.ai</span></footer>
</body></html>`

const homeHTML = `<h1>{{.Org}}</h1>
<p class="muted">{{len .Repos}} repositor{{if eq (len .Repos) 1}}y{{else}}ies{{end}}</p>
{{if .Repos}}<div class="panel">
{{range .Repos}}<div class="row">
<div style="flex:1"><a href="{{$.Base}}/{{$.Org}}/{{.Name}}">{{.Name}}</a>
{{if .Description}}<div class="muted">{{.Description}}</div>{{end}}</div>
<span class="pill">{{.DefaultBranch}}</span>
<span class="muted">{{.Size}}</span>
</div>{{end}}
</div>{{else}}<div class="panel"><div class="empty">No repositories yet.</div></div>{{end}}`

const repoHTML = `<h1>{{.Repo}}</h1>
{{if .Description}}<p class="muted">{{.Description}}</p>{{end}}
<div class="clone"><input readonly value="git clone {{.CloneHTTP}}"></div>
<div class="clone"><input readonly value="{{.CloneSSH}}"></div>
{{if .Empty}}<div class="panel"><div class="empty">This repository is empty. Push a commit to <span class="mono">{{.Ref}}</span>.</div></div>
{{else}}
<h2>{{.Ref}} · <a href="{{.Base}}/{{.Org}}/{{.Repo}}/commits?ref={{.Ref}}">commits</a>{{range .Branches}} · <a href="{{$.Base}}/{{$.Org}}/{{$.Repo}}?ref={{.}}">{{.}}</a>{{end}}</h2>
<div class="panel">
{{range .Entries}}<div class="row">
<span class="ico">{{if .IsDir}}▸{{else}}·{{end}}</span>
<a href="{{.Href}}" style="flex:1">{{.Name}}{{if .IsDir}}/{{end}}</a>
</div>{{end}}
</div>
{{if .Readme}}<div class="readme">{{.Readme}}</div>{{end}}
{{end}}`

const treeHTML = `<div class="crumbs">{{range $i,$c := .Crumbs}}{{if $i}} / {{end}}<a href="{{$c.Href}}">{{$c.Name}}</a>{{end}} <span class="pill">{{.Ref}}</span></div>
<div class="panel">
{{range .Entries}}<div class="row">
<span class="ico">{{if .IsDir}}▸{{else}}·{{end}}</span>
<a href="{{.Href}}" style="flex:1">{{.Name}}{{if .IsDir}}/{{end}}</a>
</div>{{else}}<div class="empty">Empty directory.</div>{{end}}
</div>`

const blobHTML = `<div class="crumbs">{{range $i,$c := .Crumbs}}{{if $i}} / {{end}}<a href="{{$c.Href}}">{{$c.Name}}</a>{{end}} <span class="pill">{{.Ref}}</span> <span class="muted">{{.Size}}</span></div>
{{if .Binary}}<div class="panel"><div class="empty">Binary file not shown.</div></div>
{{else}}<div class="panel"><pre>{{.Content}}</pre></div>
<p class="muted">{{.Lines}} lines</p>{{end}}`

const commitsHTML = `<h1>{{.Repo}} <span class="pill">{{.Ref}}</span></h1>
<div class="panel">
{{range .Commits}}<div class="row">
<a class="mono" href="#" style="width:80px">{{.Short}}</a>
<div style="flex:1">{{.Message}}<div class="muted">{{.Author}} · {{.When}}</div></div>
</div>{{else}}<div class="empty">No commits.</div>{{end}}
</div>`
