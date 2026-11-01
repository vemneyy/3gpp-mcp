package tools

import (
	"github.com/higebu/3gpp-mcp/internal/db"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewPDFServer exposes the tools supported by the ETSI PDF corpus. It does
// not contact 3GPP archives and does not advertise DOCX-only capabilities.
func NewPDFServer(d *db.DB) *mcp.Server {
	src := NewSource(d)
	src.DatabaseOnly = true
	s := mcp.NewServer(&mcp.Implementation{Name: "3gpp-mcp", Version: "etsi"}, &mcp.ServerOptions{Instructions: "Search and browse the latest indexed ETSI PDF for each 3GPP specification. Text and clause boundaries are extracted from PDF and may contain formatting errors. The database contains one version per specification. Workflow: use list_specs to identify a specification and its metadata. Use find_sections with ordinary words or phrases to locate mentions or sections containing all requested concepts; scope=title finds topic headings. Use search only when advanced FTS syntax or proximity is needed. Read relevant hits with get_section using the returned arguments; use get_toc to inspect neighboring sections and get_references for related specifications. Cite the spec ID, version and section number from the result. Start with 5-10 hits and narrow the terms or spec_ids before paging. Search is lexical, not semantic: try standard acronyms and synonyms if no matches are found. A snippet alone is not evidence for a complete requirement."})
	mcp.AddTool(s, ListSpecsTool, HandleListSpecs(d))
	versions := *ListVersionsTool
	versions.Description = "List the version of a 3GPP specification available in the indexed database."
	mcp.AddTool(s, &versions, HandleListVersions(src))
	mcp.AddTool(s, GetTOCTool, HandleGetTOC(src))
	mcp.AddTool(s, GetSectionTool, HandleGetSection(src))
	mcp.AddTool(s, SearchTool, HandleSearch(d))
	mcp.AddTool(s, FindSectionsTool, HandleFindSections(d))
	mcp.AddTool(s, GetReferencesTool, HandleGetReferences(d))
	return s
}
