package cloud

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenListResolveUsesWebDAVProxyWithoutFSGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("Resolve should not call OpenList server, got %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	p, err := New(TypeOpenList, map[string]any{"server": srv.URL, "token": "alist-token"}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	link, err := p.Resolve(context.Background(), "/Cloud/Movie.mkv")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if link.URL != srv.URL+"/dav/Cloud/Movie.mkv" {
		t.Fatalf("url = %q", link.URL)
	}
	if !link.Proxy {
		t.Fatalf("OpenList playback should use the existing WebDAV proxy path")
	}
	if link.Headers["Authorization"] != "alist-token" {
		t.Fatalf("Authorization = %q, want token", link.Headers["Authorization"])
	}
}

func TestOpenListResolveUsesWebDAVBasicAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("Resolve should not call OpenList server, got %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	p, err := New(TypeOpenList, map[string]any{"server": srv.URL, "username": "alice", "password": "secret"}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	link, err := p.Resolve(context.Background(), "/Cloud/Movie.mkv")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:secret"))
	if link.Headers["Authorization"] != wantAuth {
		t.Fatalf("Authorization = %q, want basic auth", link.Headers["Authorization"])
	}
	if !strings.HasSuffix(link.URL, "/dav/Cloud/Movie.mkv") || !link.Proxy {
		t.Fatalf("link = %#v, want WebDAV proxy link", link)
	}
}
