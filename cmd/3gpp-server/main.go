// 3gpp-server serves an existing specification database over MCP.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/higebu/3gpp-mcp/internal/buildinfo"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/higebu/3gpp-mcp/internal/db"
	"github.com/higebu/3gpp-mcp/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = buildinfo.Version

func main() {
	showVersion := flag.Bool("version", false, "Print version and exit")
	token := flag.String("bearer-token", os.Getenv("THREEGPP_MCP_BEARER_TOKEN"), "Optional HTTP bearer token (or THREEGPP_MCP_BEARER_TOKEN)")
	database := flag.String("db", "3gpp.db", "SQLite database path")
	transport := flag.String("transport", "stdio", "stdio or http")
	addr := flag.String("addr", ":8080", "HTTP listen address")
	flag.Parse()
	if *showVersion {
		fmt.Println("3gpp-server " + version)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	d, err := db.Open(*database)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()
	server := tools.NewPDFServer(d, version)
	switch *transport {
	case "stdio":
		err = server.Run(ctx, &mcp.StdioTransport{})
	case "http":
		httpServer := &http.Server{Addr: *addr, Handler: tools.NewPDFHTTPHandler(d, server, *token), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 70*time.Second)
			defer cancel()
			_ = httpServer.Shutdown(shutdown)
		}()
		err = httpServer.ListenAndServe()
		if err == http.ErrServerClosed {
			err = nil
		}
	default:
		log.Fatalf("unknown transport %q", *transport)
	}
	if err != nil {
		log.Fatal(err)
	}
}
