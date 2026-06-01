// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package mcp

// qmd_server.go — minimal MCP server that speaks OpenClaw's MCPorter
// QMD wire protocol (ADR 048 PR #3). MCPorter dials a server by name
// and calls a hardcoded tool `query` whose response shape is
// {results: [{docid, score, snippet, collection?, file?, start_line?, end_line?}]}.
// OpenClaw then merges those results into its own memory_search
// alongside the user's conversational chunks.
//
// This is a SEPARATE MCP endpoint from the main PackServer because:
//   - MCPorter expects the tool name to be exactly `query` (or v1
//     fallbacks `search`/`vector_search`/`deep_search`). The main
//     PackServer uses dotted pack names (`blog.publish`, etc.) and
//     can't host a bare `query` without polluting the namespace.
//   - The corpus layer should be query-only and unaware of packs;
//     keeping it on its own endpoint lets us evolve it without
//     touching the pack registry or its option surface.
//
// Wired by registerMCPQMDSSERoute in internal/api/mcp_qmd_sse.go at
// /api/v1/mcp/qmd/sse. The route is gated on a wired memory store;
// without memory the endpoint returns 503 (matches the pattern
// other optional MCP routes use).

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/tosin2013/helmdeck/internal/auth"
	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// qmdProtocolVersion is the MCP protocol version we declare in the
// `initialize` response. We mirror whatever the client sent so old
// MCPorter builds and new ones both succeed; this constant is the
// fallback we send when the client omits a version.
const qmdProtocolVersion = "2024-11-05"

// qmdServerName is the logical name MCPorter sees in its tools-list
// response. Operators configure OpenClaw with
// `memory.qmd.mcporter.serverName = "helmdeck"`; the constant matches
// so the daemon's `helmdeck.query` selector resolves correctly.
const qmdServerName = "helmdeck"

// qmdDefaultLimit caps how many corpus rows we return when the
// caller omits `limit`. MCPorter sends limit explicitly today, but
// the default keeps a hand-rolled curl call from accidentally
// returning a multi-megabyte page.
const qmdDefaultLimit = 20

// QMDServer is the minimal MCP server type the QMD bridge uses.
// Holds the wired memory store + a reusable corpus projector. One
// instance per SSE session is overkill; the type is safe to share
// across sessions because every Serve gets its own reader/writer.
type QMDServer struct {
	store memory.MemoryStore
}

// NewQMDServer constructs a QMDServer bound to the given memory
// store. Returns nil when store is nil — caller (the SSE route)
// uses that signal to mount a 503 stub instead of a live endpoint.
func NewQMDServer(store memory.MemoryStore) *QMDServer {
	if store == nil {
		return nil
	}
	return &QMDServer{store: store}
}

// Serve speaks line-delimited JSON-RPC 2.0 over r/w. Mirrors
// PackServer.Serve's framing pattern so the SSE handler can adapt
// the same way. One call per session.
func (s *QMDServer) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var writeMu sync.Mutex
	writeFrame := func(v any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		buf, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf = append(buf, '\n')
		_, err = w.Write(buf)
		return err
	}

	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var req rpcRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			_ = writeFrame(rpcResponse{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &rpcError{Code: -32700, Message: "parse error"},
			})
			continue
		}
		// Notifications are fire-and-forget per JSON-RPC 2.0.
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue
		}
		resp := s.dispatch(ctx, req)
		if err := writeFrame(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *QMDServer) dispatch(ctx context.Context, req rpcRequest) rpcResponse {
	mk := func(result any, err *rpcError) rpcResponse {
		out := rpcResponse{JSONRPC: "2.0", ID: req.ID}
		if err != nil {
			out.Error = err
			return out
		}
		raw, mErr := json.Marshal(result)
		if mErr != nil {
			out.Error = &rpcError{Code: -32603, Message: "marshal: " + mErr.Error()}
			return out
		}
		out.Result = raw
		return out
	}

	switch req.Method {
	case "initialize":
		return mk(map[string]any{
			"protocolVersion": qmdProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": qmdServerName, "version": "1.0.0"},
		}, nil)

	case "tools/list":
		return mk(map[string]any{
			"tools": []map[string]any{
				{
					"name":        "query",
					"description": "MCPorter-compatible query over helmdeck's per-caller memory: audit history (pack_history / pipeline_history) plus agent-written user facts. Returns QMD-shaped chunks with docid, snippet, collection. Scoring is keyword/substring today (no embeddings inside helmdeck — semantic recall happens client-side via OpenClaw's embedding provider).",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"searches": map[string]any{
								"type": "array",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"type":  map[string]any{"type": "string", "enum": []string{"lex", "vec", "hyde"}},
										"query": map[string]any{"type": "string"},
									},
								},
							},
							"query":       map[string]any{"type": "string"},
							"limit":       map[string]any{"type": "number"},
							"collections": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						},
					},
				},
			},
		}, nil)

	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return mk(nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()})
		}
		if params.Name != "query" {
			return mk(nil, &rpcError{Code: -32601, Message: "unknown tool: " + params.Name + " (only 'query' is supported on the QMD endpoint)"})
		}
		// Propagate the JWT caller into the engine context so
		// queryHandler reads the right namespace. Same pattern
		// PackServer.dispatch uses (server.go:660-664) — without it,
		// CallerFromContext falls back to "unknown" and the bridge
		// returns empty for any real-caller request. Only override
		// when auth claims are present so tests that pre-set the
		// caller via packs.WithCaller continue to work.
		callCtx := ctx
		if c := auth.FromContext(ctx); c != nil && c.Subject != "" {
			callCtx = packs.WithCaller(ctx, c.Subject)
		}
		results, qerr := s.queryHandler(callCtx, params.Arguments)
		if qerr != nil {
			return mk(nil, qerr)
		}
		body, mErr := json.Marshal(map[string]any{"results": results})
		if mErr != nil {
			return mk(nil, &rpcError{Code: -32603, Message: "marshal results: " + mErr.Error()})
		}
		// Wrap in MCP tool-call content array. MCPorter tolerates both
		// raw structuredContent and the standard MCP text-content shape;
		// we send both so newer and older clients both work.
		return mk(map[string]any{
			"structuredContent": map[string]any{"results": results},
			"content": []map[string]any{
				{"type": "text", "text": string(body)},
			},
		}, nil)

	default:
		return mk(nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method})
	}
}

// queryRequest is the input shape MCPorter sends in tools/call.
// Both v2 (searches[]) and v1 (single query) are accepted so we
// don't require the client to negotiate version. Whichever fields
// are present win; if both, searches[] takes precedence.
type queryRequest struct {
	Searches    []querySearchTerm `json:"searches,omitempty"`
	Query       string            `json:"query,omitempty"`
	Limit       int               `json:"limit,omitempty"`
	Collections []string          `json:"collections,omitempty"`
}

type querySearchTerm struct {
	Type  string `json:"type"`
	Query string `json:"query"`
}

// queryResult mirrors what MCPorter expects per the wire-contract
// (qmd-manager.ts:2167–2205). Optional fields are omitted when zero
// so the response stays compact for callers that don't need them.
type queryResult struct {
	DocID      string  `json:"docid"`
	Score      float64 `json:"score"`
	Snippet    string  `json:"snippet"`
	Collection string  `json:"collection,omitempty"`
	File       string  `json:"file,omitempty"`
	StartLine  int     `json:"start_line,omitempty"`
	EndLine    int     `json:"end_line,omitempty"`
}

// queryHandler reads the caller's bare-namespace memory entries,
// projects them into QMD chunks, and returns the best matches by
// substring score against the lex query. No embeddings.
func (s *QMDServer) queryHandler(ctx context.Context, args json.RawMessage) ([]queryResult, *rpcError) {
	var in queryRequest
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid arguments: " + err.Error()}
	}
	// Resolve the query string: prefer the v2 lex search term, then
	// any other search term, then the v1 single-query field. Empty
	// query returns the most-recent N entries (useful warm-up).
	q := ""
	for _, st := range in.Searches {
		if st.Type == "lex" && strings.TrimSpace(st.Query) != "" {
			q = strings.TrimSpace(st.Query)
			break
		}
	}
	if q == "" {
		for _, st := range in.Searches {
			if strings.TrimSpace(st.Query) != "" {
				q = strings.TrimSpace(st.Query)
				break
			}
		}
	}
	if q == "" {
		q = strings.TrimSpace(in.Query)
	}
	limit := in.Limit
	if limit <= 0 || limit > 100 {
		limit = qmdDefaultLimit
	}
	caller := packs.CallerFromContext(ctx)
	entries, err := s.store.List(ctx, caller, "")
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: "list memory: " + err.Error()}
	}
	results := projectCorpus(entries, q, limit)
	return results, nil
}

// projectCorpus is the helmdeck → QMD projection. Walks memory
// entries, renders each one into a QMD-shaped snippet, and ranks by
// keyword/substring match against q. Audit categories (pack_history /
// pipeline_history) get a per-row formatter that summarises the
// stored audit JSON; user_facts (and other agent-written categories)
// surface the raw value verbatim.
func projectCorpus(entries []memory.Entry, q string, limit int) []queryResult {
	results := make([]queryResult, 0, len(entries))
	for _, e := range entries {
		if e.Category == "" {
			continue
		}
		chunk := formatCorpusChunk(e)
		score := scoreChunk(chunk.Snippet, q)
		if score <= 0 && q != "" {
			continue
		}
		chunk.Score = score
		results = append(results, chunk)
	}
	// Sort by score desc; ties broken by most-recent-first via the
	// embedded EndLine (we re-use it for the unix timestamp slot so
	// the projection round-trips a stable order without an extra
	// field on the wire).
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].EndLine > results[j].EndLine
	})
	if len(results) > limit {
		results = results[:limit]
	}
	// Final pass: strip the EndLine timestamp hack so the wire only
	// shows real line-bounds (0 by default, omitted via omitempty).
	for i := range results {
		results[i].EndLine = 0
	}
	return results
}

// formatCorpusChunk renders one memory.Entry into a QMD chunk. The
// snippet shape is small markdown so a chat agent reading the
// corpus result understands the context (audit row vs user fact).
func formatCorpusChunk(e memory.Entry) queryResult {
	collection := "helmdeck-" + e.Category
	docid := e.Category + "/" + e.Key
	switch e.Category {
	case packs.AuditCategoryPack:
		var a packs.PackAudit
		if err := json.Unmarshal(e.Value, &a); err != nil {
			return queryResult{DocID: docid, Snippet: "## " + e.Key + "\n\n(audit row unreadable)", Collection: collection, EndLine: int(e.CreatedAt.Unix())}
		}
		return queryResult{
			DocID:      docid,
			Snippet:    formatPackAuditChunk(a),
			Collection: collection,
			File:       "audit/pack/" + a.Pack,
			EndLine:    int(e.CreatedAt.Unix()), // timestamp slot for stable sort; stripped before wire
		}
	case packs.AuditCategoryPipeline:
		var a packs.PipelineAudit
		if err := json.Unmarshal(e.Value, &a); err != nil {
			return queryResult{DocID: docid, Snippet: "## " + e.Key + "\n\n(audit row unreadable)", Collection: collection, EndLine: int(e.CreatedAt.Unix())}
		}
		return queryResult{
			DocID:      docid,
			Snippet:    formatPipelineAuditChunk(a),
			Collection: collection,
			File:       "audit/pipeline/" + a.Pipeline,
			EndLine:    int(e.CreatedAt.Unix()),
		}
	default:
		// user_facts and any other agent-written category. Show the
		// value verbatim; the agent stored a fact text intentionally.
		return queryResult{
			DocID:      docid,
			Snippet:    fmt.Sprintf("## %s\n\n%s\n\n(category: %s)", e.Key, string(e.Value), e.Category),
			Collection: collection,
			File:       "facts/" + e.Key,
			EndLine:    int(e.UpdatedAt.Unix()),
		}
	}
}

func formatPackAuditChunk(a packs.PackAudit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Pack call: %s\n\n", a.Pack)
	fmt.Fprintf(&b, "Outcome: %s\n", a.Outcome)
	if a.Version != "" {
		fmt.Fprintf(&b, "Version: %s\n", a.Version)
	}
	if a.DurationMs > 0 {
		fmt.Fprintf(&b, "Duration: %dms\n", a.DurationMs)
	}
	if len(a.LearnInputs) > 0 {
		b.WriteString("\nInputs used:\n")
		keys := make([]string, 0, len(a.LearnInputs))
		for k := range a.LearnInputs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "- %s: %s\n", k, a.LearnInputs[k])
		}
	}
	return b.String()
}

func formatPipelineAuditChunk(a packs.PipelineAudit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Pipeline run: %s\n\n", a.Pipeline)
	fmt.Fprintf(&b, "Outcome: %s\n", a.Outcome)
	if a.RunID != "" {
		fmt.Fprintf(&b, "Run ID: %s\n", a.RunID)
	}
	if a.DurationMs > 0 {
		fmt.Fprintf(&b, "Duration: %dms\n", a.DurationMs)
	}
	if len(a.LearnInputs) > 0 {
		b.WriteString("\nInputs used:\n")
		keys := make([]string, 0, len(a.LearnInputs))
		for k := range a.LearnInputs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "- %s: %s\n", k, a.LearnInputs[k])
		}
	}
	return b.String()
}

// scoreChunk returns a simple substring-overlap score in [0,1].
// An empty query returns 0.5 so warm-up calls still surface entries
// (sorted by recency in projectCorpus). Case-insensitive.
func scoreChunk(snippet, q string) float64 {
	if q == "" {
		return 0.5
	}
	lo := strings.ToLower(snippet)
	qlo := strings.ToLower(q)
	if strings.Contains(lo, qlo) {
		// Exact substring match — strong signal.
		return 0.9
	}
	// Word-level overlap fallback.
	qwords := strings.Fields(qlo)
	if len(qwords) == 0 {
		return 0
	}
	hits := 0
	for _, w := range qwords {
		if len(w) < 2 {
			continue
		}
		if strings.Contains(lo, w) {
			hits++
		}
	}
	if hits == 0 {
		return 0
	}
	return float64(hits) / float64(len(qwords))
}
