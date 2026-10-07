package install

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Ominous-Josef/dopt/internal/manifest"
	"github.com/Ominous-Josef/dopt/internal/registry"
)

// vendor is a fake download server whose "latest" link redirects to the current release.
type vendor struct {
	mu      sync.Mutex
	current string            // e.g. "app-1.0.tar.gz"
	files   map[string]string // name -> local archive
	srv     *httptest.Server
}

func newVendor(t *testing.T) *vendor {
	v := &vendor{files: map[string]string{}}
	v.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		defer v.mu.Unlock()
		name := strings.TrimPrefix(r.URL.Path, "/files/")
		if r.URL.Path == "/latest" {
			http.Redirect(w, r, "/files/"+v.current, http.StatusFound)
			return
		}
		p, ok := v.files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, p)
	}))
	t.Cleanup(v.srv.Close)
	return v
}

func (v *vendor) publish(name, archive string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.files[name], v.current = archive, name
}

func (h *harness) arch() string {
	a, _ := manifest.HostArch()
	return a
}

func TestCheckAndUpdate(t *testing.T) {
	h := newHarness(t)
	v := newVendor(t)
	v.publish("app-1.0.tar.gz", h.pkg("v1", true))

	req := h.req("")
	req.Download, req.URL = true, v.srv.URL+"/latest"
	must(t, h.run("y\nn\n", req), h)

	if _, err := os.Stat(h.cwd + "/app-1.0.tar.gz"); err != nil {
		t.Errorf("download not kept under the redirected name: %v", err)
	}
	kept, _ := os.ReadDir(h.cwd)

	st := Check(context.Background(), h.l, appID, h.arch())
	if st.State != UpToDate || st.Installed != "1.0" || st.Available != "1.0" {
		t.Fatalf("after install: %+v", st)
	}

	v.publish("app-1.1.tar.gz", h.pkg("v2", true))
	st = Check(context.Background(), h.l, appID, h.arch())
	if st.State != UpdateAvailable || st.Installed != "1.0" || st.Available != "1.1" {
		t.Fatalf("after a new release: %+v", st)
	}

	upd := UpdateRequest(h.l, st)
	upd.Cleanup = true
	must(t, h.run("", upd), h)
	if h.version() != "v2" {
		t.Errorf("update didn't install the new release:\n%s", h.output())
	}
	if st := Check(context.Background(), h.l, appID, h.arch()); st.State != UpToDate || st.Installed != "1.1" {
		t.Errorf("after update: %+v", st)
	}
	if entries, _ := os.ReadDir(h.cwd); len(entries) != len(kept) {
		t.Errorf("update with Cleanup kept its download: %v", entries)
	}
}

func TestCheckUnavailableAndUnknown(t *testing.T) {
	h := newHarness(t)
	// Installed from a local file: nothing to check against.
	must(t, h.run("y\nn\n", h.req(h.pkg("v1", true))), h)
	if st := Check(context.Background(), h.l, appID, h.arch()); st.State != Unavailable || !strings.Contains(st.Reason, "local archive") {
		t.Errorf("local install: %+v", st)
	}

	// Installed by dopt-bash or dopt 2: no saved recipe.
	registry.RemoveRecipe(h.l.RegistryDir, appID)
	if st := Check(context.Background(), h.l, appID, h.arch()); st.State != Unavailable || !strings.Contains(st.Reason, "no saved recipe") {
		t.Errorf("no recipe: %+v", st)
	}

	// A fixed, unversioned URL whose server sends no ETag or Last-Modified.
	h2 := newHarness(t)
	archive := h2.pkg("v1", true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := os.ReadFile(archive)
		w.Write(data)
	}))
	t.Cleanup(srv.Close)
	req := h2.req("")
	req.Download, req.URL = true, srv.URL+"/download"
	must(t, h2.run("y\nn\n", req), h2)
	if st := Check(context.Background(), h2.l, appID, h2.arch()); st.State != Unknown {
		t.Errorf("unversioned URL: %+v", st)
	}
}

func TestCheckAllKeepsOrder(t *testing.T) {
	h := newHarness(t)
	ids := []string{"a.one", "b.two", "c.three"}
	got := CheckAll(context.Background(), h.l, ids, h.arch())
	for i, st := range got {
		if st.AppID != ids[i] || st.State != Unavailable {
			t.Errorf("%d: %+v", i, st)
		}
	}
}
