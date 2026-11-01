// 3gpp-server serves an existing specification database over MCP.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	"github.com/higebu/3gpp-mcp/internal/db"
	"github.com/higebu/3gpp-mcp/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	database := flag.String("db", "3gpp.db", "SQLite database path")
	transport := flag.String("transport", "stdio", "stdio or http")
	addr := flag.String("addr", ":8080", "HTTP listen address")
	flag.Parse()
	d, err := db.Open(*database)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()
	server := tools.NewPDFServer(d)
	switch *transport {
	case "stdio":
		err = server.Run(context.Background(), &mcp.StdioTransport{})
	case "http":
		err = http.ListenAndServe(*addr, tools.NewStreamableHTTPHandler(server))
	default:
		log.Fatalf("unknown transport %q", *transport)
	}
	if err != nil {
		log.Fatal(err)
	}
}
