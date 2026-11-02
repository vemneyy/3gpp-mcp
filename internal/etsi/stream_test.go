package etsi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStreamOverlapsDiscoveryAndDownload(t *testing.T) {
	downloaded := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/etsi_ts/":
			fmt.Fprint(w, `<a href="123500_123599/">group</a>`)
		case "/etsi_ts/123500_123599/":
			fmt.Fprint(w, `<a href="123501/">first</a><a href="123502/">slow</a>`)
		case "/etsi_tr/":
			fmt.Fprint(w, `<a href="123800_123899/">group</a>`)
		case "/etsi_tr/123800_123899/", "/etsi_en/", "/etsi_gts/":
			fmt.Fprint(w, `<a href="ignored/">ignored</a>`)
		case "/etsi_ts/123500_123599/123502/":
			select {
			case <-downloaded:
				fmt.Fprint(w, `<a href="18.02.00_60/">version</a>`)
			case <-r.Context().Done():
				return
			}
		case "/etsi_ts/123500_123599/123501/":
			fmt.Fprint(w, `<a href="18.02.00_60/">version</a>`)
		default:
			if strings.HasSuffix(r.URL.Path, "p.pdf") {
				once.Do(func() { close(downloaded) })
				fmt.Fprint(w, "%PDF-1.7")
				return
			}
			if strings.Contains(r.URL.Path, "123501/") {
				fmt.Fprint(w, `<a href="ts_123501v180200p.pdf">pdf</a>`)
			} else {
				fmt.Fprint(w, `<a href="ts_123502v180200p.pdf">pdf</a>`)
			}
		}
	}))
	defer server.Close()
	client := Client{Base: server.URL, HTTP: server.Client()}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	count := 0
	err := client.Stream(ctx, 2, func(doc Document) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, doc.URL, nil)
		if err != nil {
			return err
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		count++
		return nil
	})
	if err != nil || count != 2 {
		t.Fatalf("streamed %d documents: %v", count, err)
	}
	sentinel := errors.New("stop consumer")
	err = client.Stream(t.Context(), 2, func(Document) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("consumer error lost: %v", err)
	}
}
