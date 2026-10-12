package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"blocky-ctrl/internal/blocky"
	"blocky-ctrl/internal/config"
)

// fakeBlocky is a minimal blocky API that records the calls it gets.
type fakeBlocky struct {
	mu        sync.Mutex
	calls     []string
	enabled   bool
	statsDown bool
}

func (f *fakeBlocky) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI())
	switch r.URL.Path {
	case "/api/blocking/status":
		json.NewEncoder(w).Encode(map[string]any{"enabled": f.enabled})
	case "/api/blocking/enable":
		f.enabled = true
	case "/api/blocking/disable":
		f.enabled = false
	case "/api/lists/refresh", "/api/cache/flush":
	case "/api/stats":
		if f.statsDown {
			http.Error(w, "statistics are disabled", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, `{
			"start": "2026-10-10T08:00:00Z",
			"summary": {"queries": 100, "blocked": 10, "cached": 30, "avgResponseMs": 4, "cacheHitRate": 0.5},
			"perHour": [
				{"hour": "2026-10-10T08:00:00Z", "queries": 99, "blocked": 9},
				{"hour": "2026-10-11T09:00:00Z", "queries": 40, "blocked": 4},
				{"hour": "2026-10-11T12:00:00Z", "queries": 60, "blocked": 6}
			],
			"topBlockedDomains": [{"name":"a","count":6},{"name":"b","count":5},{"name":"c","count":4},{"name":"d","count":3},{"name":"e","count":2},{"name":"f","count":1}],
			"topClients": null,
			"lists": {"denylist": {"malware": 1, "ads": 3}, "allowlist": {"ads": 1}}
		}`)
	default:
		http.NotFound(w, r)
	}
}

var testStatic = fstest.MapFS{
	"index.html": {Data: []byte(`<link href="{{asset "app.css"}}"><script type="application/json">{{.Instances}}</script>`)},
	"app.css":    {Data: []byte(`body{}`)},
}

func setup(t *testing.T) (*httptest.Server, *fakeBlocky, *fakeBlocky) {
	t.Helper()
	main, backup := &fakeBlocky{enabled: true}, &fakeBlocky{enabled: true, statsDown: true}
	mainSrv, backupSrv := httptest.NewServer(main), httptest.NewServer(backup)
	t.Cleanup(mainSrv.Close)
	t.Cleanup(backupSrv.Close)

	cfg, err := config.Parse([]byte("instances:\n" +
		"  - name: Main\n    url: " + mainSrv.URL + "\n" +
		"  - name: Backup\n    url: " + backupSrv.URL + "\n    readonly: true\n    label: Failover\n"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, testStatic, "test")
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 10, 11, 12, 30, 0, 0, time.UTC) }
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, main, backup
}

func do(t *testing.T, method, url, body string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, url, err)
		}
	}
	return resp.StatusCode
}

func TestIndex(t *testing.T) {
	srv, _, _ := setup(t)
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(b)
	for _, want := range []string{
		`href="/app.css?v=`,
		`{"id":"main","name":"Main","host":"127.0.0.1","readonly":false}`,
		`"id":"backup","name":"Backup",`,
		`"readonly":true,"label":"Failover"}]`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("index lacks %q:\n%s", want, page)
		}
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("index Cache-Control = %q", cc)
	}

	asset := strings.Split(strings.Split(page, `href="`)[1], `"`)[0]
	resp, _ = http.Get(srv.URL + asset)
	resp.Body.Close()
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("hashed asset Cache-Control = %q", cc)
	}
	for _, p := range []string{"/index.html", "/nope.js", "/../go.mod"} {
		resp, _ := http.Get(srv.URL + p)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, resp.StatusCode)
		}
	}
}

func TestState(t *testing.T) {
	srv, _, _ := setup(t)

	var st State
	if code := do(t, "GET", srv.URL+"/api/instances/main", "", &st); code != 200 {
		t.Fatalf("status %d", code)
	}
	if st.Status == nil || !st.Status.Enabled || st.Error != "" {
		t.Fatalf("status: %+v", st)
	}
	s := st.Stats
	if s == nil {
		t.Fatalf("no stats: %+v", st)
	}
	if s.Queries != 100 || s.Blocked != 10 || s.Cached != 30 || s.AvgMs != 4 {
		t.Errorf("summary: %+v", s)
	}
	if len(s.Hours) != 24 {
		t.Fatalf("%d hours", len(s.Hours))
	}
	// Hours run from 13:00 yesterday to 12:00 today; 08:00 yesterday is out of the window.
	if s.Hours[23] != (Hour{60, 6}) || s.Hours[20] != (Hour{40, 4}) || s.Hours[0] != (Hour{}) {
		t.Errorf("hours: %+v", s.Hours)
	}
	if len(s.TopBlocked) != topN || s.TopBlocked[0].Name != "a" {
		t.Errorf("topBlocked: %+v", s.TopBlocked)
	}
	if s.FirstHour != 0 {
		t.Errorf("firstHour = %d, want 0", s.FirstHour)
	}
	if s.TopClients == nil {
		t.Error("topClients should be an empty list, not null")
	}
	if len(s.Groups) != 2 || s.Groups[0] != (Group{"ads", 3, 1}) || s.Groups[1] != (Group{"malware", 1, 0}) {
		t.Errorf("groups: %+v", s.Groups)
	}

	st = State{}
	do(t, "GET", srv.URL+"/api/instances/backup", "", &st)
	if st.Status != nil || st.Stats != nil || st.Error != "statistics disabled (statistics.enable)" {
		t.Errorf("backup without stats: %+v", st)
	}

	if code := do(t, "GET", srv.URL+"/api/instances/nope", "", nil); code != 404 {
		t.Errorf("unknown instance: %d", code)
	}
}

func TestUnreachable(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	cfg, _ := config.Parse([]byte("instances:\n  - name: Dead\n    url: http://user:secret@" + dead.Listener.Addr().String() + "\n"))
	s, err := New(cfg, testStatic, "test")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	var st State
	do(t, "GET", srv.URL+"/api/instances/dead", "", &st)
	if st.Error == "" || st.Status != nil || st.Stats != nil {
		t.Errorf("state: %+v", st)
	}
	if strings.Contains(st.Error, "secret") {
		t.Errorf("error leaks credentials: %q", st.Error)
	}
}

func TestActions(t *testing.T) {
	srv, main, backup := setup(t)
	base := srv.URL + "/api/instances/main"

	var status struct{ Enabled bool }
	if code := do(t, "POST", base+"/disable", `{"seconds":300,"groups":["ads"]}`, &status); code != 200 || status.Enabled {
		t.Fatalf("disable: %d %+v", code, status)
	}
	if got := main.calls[len(main.calls)-2]; got != "GET /api/blocking/disable?duration=5m0s&groups=ads" {
		t.Errorf("blocky got %q", got)
	}
	do(t, "POST", base+"/disable", `{}`, &status)
	if got := main.calls[len(main.calls)-2]; got != "GET /api/blocking/disable" {
		t.Errorf("disable indefinitely: blocky got %q", got)
	}
	if code := do(t, "POST", base+"/enable", `{}`, &status); code != 200 || !status.Enabled {
		t.Fatalf("enable: %d %+v", code, status)
	}
	for act, want := range map[string]string{"refresh-lists": "POST /api/lists/refresh", "flush-cache": "POST /api/cache/flush"} {
		if code := do(t, "POST", base+"/"+act, `{}`, nil); code != 200 {
			t.Errorf("%s: %d", act, code)
		}
		if got := main.calls[len(main.calls)-2]; got != want {
			t.Errorf("%s: blocky got %q", act, got)
		}
	}

	for _, body := range []string{`{"seconds":-1}`, `{"seconds":999999999}`, `{"groups":["a,b"]}`, `{"duration":"5m"}`, `nope`} {
		if code := do(t, "POST", base+"/disable", body, nil); code != 400 {
			t.Errorf("disable %s: %d, want 400", body, code)
		}
	}

	// Read-only instances can't be toggled, but lists and cache are theirs.
	n := len(backup.calls)
	for _, act := range []string{"enable", "disable"} {
		if code := do(t, "POST", srv.URL+"/api/instances/backup/"+act, `{}`, nil); code != http.StatusConflict {
			t.Errorf("read-only %s: %d, want 409", act, code)
		}
	}
	if len(backup.calls) != n {
		t.Errorf("read-only got calls: %v", backup.calls[n:])
	}
	if code := do(t, "POST", srv.URL+"/api/instances/backup/flush-cache", `{}`, nil); code != 200 {
		t.Errorf("read-only flush: %d", code)
	}

	var e struct{ Error string }
	if code := do(t, "GET", base+"/enable", "", &e); code != http.StatusNotFound || e.Error == "" {
		t.Errorf("GET enable: %d", code)
	}
}

func TestCrossSiteRefused(t *testing.T) {
	srv, main, _ := setup(t)
	req, _ := http.NewRequest("POST", srv.URL+"/api/instances/main/disable", strings.NewReader(`{}`))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site POST: %d", resp.StatusCode)
	}
	if len(main.calls) != 0 {
		t.Errorf("blocky got %v", main.calls)
	}
}

func TestShapeStatsYoungInstance(t *testing.T) {
	s := &Server{now: func() time.Time { return time.Date(2026, 10, 11, 20, 30, 0, 0, time.UTC) }}
	var raw blocky.Stats
	raw.Start = time.Date(2026, 10, 11, 18, 6, 0, 0, time.UTC)
	got := s.shapeStats(raw)
	// Hours run 21:00 yesterday .. 20:00 today; blocky started in the 18:00 bucket.
	if got.FirstHour != 21 {
		t.Errorf("firstHour = %d, want 21", got.FirstHour)
	}
}
