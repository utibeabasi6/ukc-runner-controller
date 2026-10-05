package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"

	"github.com/utibeabasi6/ukc-runner-controller/internal/controller"
	"github.com/utibeabasi6/ukc-runner-controller/internal/store"
)

//go:embed static
var staticFiles embed.FS

var assetVersions = func() map[string]string {
	versions := make(map[string]string)
	err := fs.WalkDir(staticFiles, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := staticFiles.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		versions[strings.TrimPrefix(path, "static/")] = hex.EncodeToString(sum[:6])
		return nil
	})
	if err != nil {
		panic(err)
	}
	return versions
}()

func asset(name string) string {
	return "/static/" + name + "?v=" + assetVersions[name]
}

type Options struct {
	Store      *store.Store
	Controller *controller.Controller
	Logger     *slog.Logger
	Version    string
	// Username and Password enable HTTP basic auth on every page except the
	// health check. Both empty leaves the dashboard open.
	Username string
	Password string
}

type Server struct {
	store    *store.Store
	ctrl     *controller.Controller
	log      *slog.Logger
	version  string
	username string
	password string

	logsMu sync.Mutex
	logs   map[string]cachedLogs
}

type cachedLogs struct {
	text string
	at   time.Time
}

// logCacheTTL bounds console reads to one API call per instance in this
// window, however many dashboard tabs refresh the runner page.
const logCacheTTL = 15 * time.Second

func New(opts Options) *Server {
	return &Server{
		store:    opts.Store,
		ctrl:     opts.Controller,
		log:      opts.Logger,
		version:  opts.Version,
		username: opts.Username,
		password: opts.Password,
		logs:     make(map[string]cachedLogs),
	}
}

func (s *Server) Handler() http.Handler {
	static, _ := fs.Sub(staticFiles, "static")
	files := http.StripPrefix("/static/", http.FileServerFS(static))

	pages := http.NewServeMux()
	pages.HandleFunc("GET /{$}", s.overview)
	pages.HandleFunc("GET /jobs", s.jobs)
	pages.HandleFunc("GET /jobs/{id}", s.job)
	pages.HandleFunc("GET /runners", s.runners)
	pages.HandleFunc("GET /runners/{name}", s.runner)
	pages.HandleFunc("GET /scale-sets", s.scaleSets)
	pages.HandleFunc("GET /static/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Has("v") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		files.ServeHTTP(w, r)
	})
	pages.HandleFunc("/", s.notFound)

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	root.Handle("/", s.authenticate(secureHeaders(pages)))
	return root
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	if s.username == "" && s.password == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.username)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(s.password)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="ukc-runner-controller", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, title, nav string, content templ.Component) {
	c := content
	if r.Header.Get("X-Partial") != "1" {
		c = layout(title, nav, s.version, content)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		s.log.Error("rendering page", "path", r.URL.Path, "error", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("serving page", "path", r.URL.Path, "error", err)
	s.render(w, r, http.StatusInternalServerError, "Error", "", errorPage("500", "The controller could not load this page. Check its logs for details."))
}
