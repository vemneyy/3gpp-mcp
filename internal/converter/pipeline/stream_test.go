package pipeline

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The slow directory cannot finish before a download starts. A collect-then-
// download implementation deadlocks until the deadline, even with many workers.
func TestDiscoveryOverlapsDownload(t *testing.T) {
	var capture bytes.Buffer
	zw := zip.NewWriter(&capture)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/ftp/Specs/archive/")
		switch path {
		case "":
			fmt.Fprint(w, `<a href="23_series/">23_series</a>`)
		case "23_series/":
			fmt.Fprint(w, `<a href="23.501/">23.501</a><a href="23.502/">23.502</a>`)
		case "23_series/23.501/":
			fmt.Fprint(w, `<a href="23501-j10.zip">23501-j10.zip</a>`)
		case "23_series/23.502/":
			select {
			case <-started:
				fmt.Fprint(w, `<a href="?sortby=name">Name</a><a href="?sortby=size">Size</a>`)
			case <-r.Context().Done():
				return
			}
		case "23_series/23.501/23501-j10.zip":
			once.Do(func() { close(started) })
			_, _ = w.Write(capture.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &http.Client{Transport: &redirectTransport{base: http.DefaultTransport, testURL: server.URL}}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	p := &Pipeline{DB: setupTestDB(t), Client: client, Workers: 2, Timeout: time.Second}
	err := p.RunSource(ctx, func(ctx context.Context, emit func(*SpecVersion) error) error {
		return Discover(ctx, client, "", SpecFilter{Series: []string{"23"}}, false, 2, emit)
	})
	select {
	case <-started:
	default:
		t.Fatal("discovery blocked download")
	}
	if ctx.Err() != nil {
		t.Fatalf("pipeline did not overlap stages: %v (%v)", ctx.Err(), err)
	}
	if err != nil {
		t.Fatal(err)
	}
}
