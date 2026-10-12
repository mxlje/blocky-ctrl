// Package server serves the embedded UI and a small JSON API that controls the
// configured blocky instances.
package server

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"slices"
	"time"

	"blocky-ctrl/internal/blocky"
	"blocky-ctrl/internal/config"
)

const (
	readTimeout    = 4 * time.Second
	actionTimeout  = 10 * time.Second
	refreshTimeout = 2 * time.Minute // lists download synchronously
	topN           = 5
)

// Instance is a configured blocky instance.
type Instance struct {
	config.Instance
	Blocky *blocky.Client
}

type Server struct {
	byID   map[string]*Instance
	static fs.FS
	assets map[string]string // file name -> content hash
	index  []byte
	now    func() time.Time
}

// New builds the server. static must hold index.html (a template) and the
// assets it references through {{asset "name"}}.
func New(cfg *config.Config, static fs.FS, version string) (*Server, error) {
	s := &Server{byID: map[string]*Instance{}, static: static, assets: map[string]string{}, now: time.Now}
	var instances []*Instance
	for _, ci := range cfg.Instances {
		bc, err := blocky.New(ci.URL)
		if err != nil {
			return nil, err
		}
		in := &Instance{Instance: ci, Blocky: bc}
		instances = append(instances, in)
		s.byID[in.ID] = in
	}

	err := fs.WalkDir(static, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(static, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		s.assets[p] = hex.EncodeToString(sum[:6])
		return nil
	})
	if err != nil {
		return nil, err
	}

	tmpl, err := template.New("index.html").Funcs(template.FuncMap{
		"asset": func(name string) (string, error) {
			h, ok := s.assets[name]
			if !ok {
				return "", errors.New("unknown asset " + name)
			}
			return "/" + name + "?v=" + h, nil
		},
	}).ParseFS(static, "index.html")
	if err != nil {
		return nil, err
	}
	type pageInstance struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Host     string `json:"host"`
		ReadOnly bool   `json:"readonly"`
		Label    string `json:"label,omitempty"`
	}
	var page struct {
		Version   string
		Instances []pageInstance
	}
	page.Version = version
	for _, in := range instances {
		page.Instances = append(page.Instances, pageInstance{in.ID, in.Name, in.Blocky.Host(), in.ReadOnly, in.Label})
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, page); err != nil {
		return nil, err
	}
	s.index = buf.Bytes()
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.serveIndex)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /api/instances/{id}", s.withInstance(s.state))
	mux.HandleFunc("POST /api/instances/{id}/enable", s.withInstance(s.enable))
	mux.HandleFunc("POST /api/instances/{id}/disable", s.withInstance(s.disable))
	mux.HandleFunc("POST /api/instances/{id}/refresh-lists", s.withInstance(s.refreshLists))
	mux.HandleFunc("POST /api/instances/{id}/flush-cache", s.withInstance(s.flushCache))
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, errors.New("not found"))
	})
	mux.HandleFunc("GET /", s.serveAsset)

	// No auth: refuse cross-site POSTs so other pages can't toggle blocking.
	return securityHeaders(http.NewCrossOriginProtection().Handler(mux))
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(s.index)
}

// serveAsset serves static files. Requests carrying the current content hash
// (as produced by {{asset}}) may be cached forever.
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request) {
	name := path.Clean(r.URL.Path)[1:]
	h, ok := s.assets[name]
	if !ok || name == "index.html" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("v") == h {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", `"`+h+`"`)
	}
	http.ServeFileFS(w, r, s.static, name)
}

func (s *Server) withInstance(fn func(http.ResponseWriter, *http.Request, *Instance)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		in, ok := s.byID[r.PathValue("id")]
		if !ok {
			writeErr(w, http.StatusNotFound, errors.New("unknown instance"))
			return
		}
		fn(w, r, in)
	}
}

// State is what the UI shows for one instance: Status and Stats, or Error.
type State struct {
	Status *blocky.BlockingStatus `json:"status,omitempty"`
	Stats  *Stats                 `json:"stats,omitempty"`
	Error  string                 `json:"error,omitempty"`
}

type Stats struct {
	Queries    int                `json:"queries"`
	Blocked    int                `json:"blocked"`
	AvgMs      int                `json:"avgMs"`
	Cached     int                `json:"cached"`
	Hours      []Hour             `json:"hours"`     // 24 hours, oldest first, the last one is the current hour
	FirstHour  int                `json:"firstHour"` // index into Hours of blocky's start; earlier hours have no data
	TopBlocked []blocky.NameCount `json:"topBlocked"`
	TopClients []blocky.NameCount `json:"topClients"`
	Groups     []Group            `json:"groups"` // denylist groups
}

type Hour struct {
	Queries int `json:"q"`
	Blocked int `json:"b"`
}

type Group struct {
	Name    string `json:"name"`
	Denied  int    `json:"denied"`
	Allowed int    `json:"allowed"`
}

func (s *Server) state(w http.ResponseWriter, r *http.Request, in *Instance) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	var raw blocky.Stats
	statsc := make(chan error, 1)
	go func() {
		var err error
		raw, err = in.Blocky.Stats(ctx)
		statsc <- err
	}()
	status, err := in.Blocky.BlockingStatus(ctx)
	err = cmp.Or(err, <-statsc)

	var st State
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Status, st.Stats = &status, s.shapeStats(raw)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, st)
}

func (s *Server) shapeStats(raw blocky.Stats) *Stats {
	now := s.now().UTC()
	cur := now.Truncate(time.Hour)
	first := cur.Add(-23 * time.Hour)
	out := &Stats{
		Queries:    raw.Summary.Queries,
		Blocked:    raw.Summary.Blocked,
		AvgMs:      raw.Summary.AvgResponseMs,
		Cached:     raw.Summary.Cached,
		Hours:      make([]Hour, 24),
		TopBlocked: head(raw.TopBlockedDomains, topN),
		TopClients: head(raw.TopClients, topN),
		Groups:     []Group{},
	}
	out.FirstHour = max(0, int(raw.Start.UTC().Truncate(time.Hour).Sub(first)/time.Hour))
	for _, p := range raw.PerHour {
		i := int(p.Hour.UTC().Sub(first) / time.Hour)
		if i >= 0 && i < len(out.Hours) {
			out.Hours[i] = Hour{p.Queries, p.Blocked}
		}
	}
	for name, n := range raw.Lists.Denylist {
		out.Groups = append(out.Groups, Group{Name: name, Denied: n, Allowed: raw.Lists.Allowlist[name]})
	}
	slices.SortFunc(out.Groups, func(a, b Group) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

func head[T any](s []T, n int) []T {
	if s == nil {
		return []T{}
	}
	return s[:min(n, len(s))]
}

var errReadOnly = errors.New("instance is read-only")

func (s *Server) enable(w http.ResponseWriter, r *http.Request, in *Instance) {
	if in.ReadOnly {
		writeErr(w, http.StatusConflict, errReadOnly)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
	defer cancel()
	s.respondStatus(ctx, w, in, in.Blocky.EnableBlocking(ctx))
}

var groupRe = regexp.MustCompile(`^[\w.-]{1,64}$`)

func (s *Server) disable(w http.ResponseWriter, r *http.Request, in *Instance) {
	if in.ReadOnly {
		writeErr(w, http.StatusConflict, errReadOnly)
		return
	}
	var req struct {
		// Seconds until blocking turns back on; 0 = until enabled again.
		Seconds int      `json:"seconds"`
		Groups  []string `json:"groups"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Seconds < 0 || req.Seconds > 7*24*3600 {
		writeErr(w, http.StatusBadRequest, errors.New("seconds must be between 0 and one week"))
		return
	}
	for _, g := range req.Groups {
		if !groupRe.MatchString(g) {
			writeErr(w, http.StatusBadRequest, errors.New("invalid group name"))
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
	defer cancel()
	d := time.Duration(req.Seconds) * time.Second
	s.respondStatus(ctx, w, in, in.Blocky.DisableBlocking(ctx, d, req.Groups))
}

func (s *Server) refreshLists(w http.ResponseWriter, r *http.Request, in *Instance) {
	ctx, cancel := context.WithTimeout(r.Context(), refreshTimeout)
	defer cancel()
	s.respondStatus(ctx, w, in, in.Blocky.RefreshLists(ctx))
}

func (s *Server) flushCache(w http.ResponseWriter, r *http.Request, in *Instance) {
	ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
	defer cancel()
	s.respondStatus(ctx, w, in, in.Blocky.FlushCache(ctx))
}

// respondStatus answers an action with the instance's fresh blocking status.
func (s *Server) respondStatus(ctx context.Context, w http.ResponseWriter, in *Instance, err error) {
	if err != nil {
		slog.Warn("action failed", "instance", in.Name, "err", err)
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	st, err := in.Blocky.BlockingStatus(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, st)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid request body"))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("write response", "err", err)
	}
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
