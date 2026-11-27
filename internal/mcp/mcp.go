// Package mcp implements a zero-dependency Model Context Protocol (MCP) stdio
// server that exposes a Loot server's HTTP JSON API as discoverable tools for
// AI agents.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	defaultProtocolVersion = "2025-03-26"
	serverInstructions     = "Loot tracks indie app metrics across stores (App Store, Google Play, Microsoft Store, Flathub, Snapcraft, RevenueCat, GitHub, Meta Ads, Cloudflare Analytics, GA4). Start with loot_report for a complete cross-app or single-app overview, then use targeted tools (loot_vault, loot_hearth, loot_trend, loot_reviews, loot_funnel, loot_recap, loot_codex, loot_drops, loot_quests, loot_mysteries, loot_bosses, loot_sources, loot_apps) for detailed breakdowns."
)

// Doer performs JSON HTTP requests against a Loot server.
type Doer interface {
	DoJSON(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error)
}

// Options configures an MCP server.
type Options struct {
	Client      Doer
	Version     string
	AllowWrites bool
}

// Server serves JSON-RPC 2.0 MCP requests over a newline-delimited stream.
type Server struct {
	client      Doer
	version     string
	allowWrites bool
	tools       []Tool
	handlers    map[string]toolHandler
}

type toolHandler func(ctx context.Context, args map[string]any) (json.RawMessage, error)

// Tool is an MCP tool definition returned by tools/list.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema map[string]any  `json:"inputSchema"`
	Annotations *ToolAnnotation `json:"annotations,omitempty"`
}

// ToolAnnotation communicates read-only / destructive hints to MCP clients.
type ToolAnnotation struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	IdempotentHint  bool `json:"idempotentHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
}

var (
	readOnlyAnno = &ToolAnnotation{
		ReadOnlyHint:    true,
		DestructiveHint: false,
		IdempotentHint:  true,
		OpenWorldHint:   false,
	}
	writeAnno = &ToolAnnotation{
		ReadOnlyHint:    false,
		DestructiveHint: false,
		IdempotentHint:  false,
		OpenWorldHint:   false,
	}
)

// New returns an MCP server wired to opts.Client.
func New(opts Options) *Server {
	ver := strings.TrimSpace(opts.Version)
	if ver == "" {
		ver = "dev"
	}
	s := &Server{
		client:      opts.Client,
		version:     ver,
		allowWrites: opts.AllowWrites,
		handlers:    map[string]toolHandler{},
	}
	s.registerTools()
	return s
}

// Serve reads newline-delimited JSON-RPC 2.0 messages from r and writes replies
// to w until EOF or ctx cancellation.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	br := bufio.NewReaderSize(r, 64*1024)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line, err := br.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			if resp := s.handleMessage(ctx, trimmed); resp != nil {
				if werr := enc.Encode(resp); werr != nil {
					return werr
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handleMessage(ctx context.Context, raw []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
		}
	}
	// JSON-RPC notifications have no id and expect no reply.
	isNotification := len(bytes.TrimSpace(req.ID)) == 0
	if isNotification {
		return nil
	}

	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		proto := negotiateProtocol(params.ProtocolVersion)
		return okResp(req.ID, map[string]any{
			"protocolVersion": proto,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "loot",
				"version": s.version,
			},
			"instructions": serverInstructions,
		})

	case "ping":
		return okResp(req.ID, map[string]any{})

	case "tools/list":
		return okResp(req.ID, map[string]any{"tools": s.tools})

	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return errResp(req.ID, -32602, "invalid params: "+err.Error())
		}
		h, ok := s.handlers[params.Name]
		if !ok {
			return errResp(req.ID, -32602, "unknown tool: "+params.Name)
		}
		if params.Arguments == nil {
			params.Arguments = map[string]any{}
		}
		data, err := h(ctx, params.Arguments)
		if err != nil {
			return okResp(req.ID, map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "Error: " + err.Error()},
				},
				"isError": true,
			})
		}
		var pretty bytes.Buffer
		text := string(data)
		if json.Indent(&pretty, data, "", "  ") == nil {
			text = pretty.String()
		}
		return okResp(req.ID, map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": text},
			},
		})

	case "resources/list":
		return okResp(req.ID, map[string]any{"resources": []any{}})

	case "prompts/list":
		return okResp(req.ID, map[string]any{"prompts": []any{}})

	default:
		return errResp(req.ID, -32601, "method not found: "+req.Method)
	}
}

func negotiateProtocol(client string) string {
	switch strings.TrimSpace(client) {
	case "2024-11-05", "2025-03-26", "2025-06-18":
		return strings.TrimSpace(client)
	default:
		return defaultProtocolVersion
	}
}

func okResp(id json.RawMessage, result any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func errResp(id json.RawMessage, code int, msg string) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

func (s *Server) addTool(t Tool, h toolHandler) {
	s.tools = append(s.tools, t)
	s.handlers[t.Name] = h
}

func schema(props map[string]any, required ...string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func strProp(desc string, enum ...string) map[string]any {
	p := map[string]any{"type": "string", "description": desc}
	if len(enum) > 0 {
		p["enum"] = enum
	}
	return p
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func numProp(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func (s *Server) registerTools() {
	appProp := strProp("Optional product name to scope results to a single app (omit for all apps).")

	s.addTool(Tool{
		Name:        "loot_report",
		Description: "Fetch a comprehensive business overview report combining headline stats, Vault revenue & scorecards, per-app active/all-time audience, ratings & recent reviews, ad funnel (if configured), active quests/mysteries/bosses, waiting chests, and source health.",
		InputSchema: schema(map[string]any{
			"range":          strProp("Vault revenue window (default: 30d).", "7d", "30d", "90d", "365d"),
			"app":            appProp,
			"reviews_range":  strProp("Ratings and reviews window (default: 90d).", "30d", "90d", "1y", "all"),
			"funnel_days":    intProp("Acquisition funnel lookback in days, 1-90 (default: 14)."),
			"include_series": boolProp("Include daily time-series arrays in vault and funnel sections (default: false for compact output)."),
		}),
		Annotations: readOnlyAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		q := url.Values{}
		setStr(q, "range", args, "range")
		setStr(q, "app", args, "app")
		setStr(q, "reviews_range", args, "reviews_range")
		setInt(q, "funnel_days", args, "funnel_days")
		if boolArg(args, "include_series") {
			q.Set("include_series", "1")
		}
		return s.client.DoJSON(ctx, http.MethodGet, "/api/report", q, nil)
	})

	s.addTool(Tool{
		Name:        "loot_vault",
		Description: "Fetch settled revenue, paid units, refunds, period-over-period comparison, daily revenue series, breakdowns by source/app/country, active subscriptions, realtime RevenueCat today, and all-time per-app scorecards.",
		InputSchema: schema(map[string]any{
			"range": strProp("Trailing revenue window (default: 30d).", "7d", "30d", "90d", "365d"),
			"app":   appProp,
		}),
		Annotations: readOnlyAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		q := url.Values{}
		setStr(q, "range", args, "range")
		setStr(q, "app", args, "app")
		return s.client.DoJSON(ctx, http.MethodGet, "/api/vault/summary", q, nil)
	})

	s.addTool(Tool{
		Name:        "loot_hearth",
		Description: "Fetch audience per app and store (active/weekly devices vs all-time arrivals), GitHub release downloads, per-country settlements and revenue, countryless fleet, and account era/XP.",
		InputSchema: schema(map[string]any{
			"app": appProp,
		}),
		Annotations: readOnlyAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		q := url.Values{}
		setStr(q, "app", args, "app")
		return s.client.DoJSON(ctx, http.MethodGet, "/api/hearth", q, nil)
	})

	s.addTool(Tool{
		Name:        "loot_trend",
		Description: "Fetch a daily time series for one Hearth metric (audience, population, settlements, revenue, fleet, downloads, country, or xp).",
		InputSchema: schema(map[string]any{
			"metric":  strProp("Metric to query.", "audience", "population", "settlements", "revenue", "fleet", "downloads", "country", "xp"),
			"range":   strProp("Time range (default: 30d).", "7d", "30d", "90d", "ytd", "1y", "all"),
			"product": strProp("Product name (required when metric is audience or downloads)."),
			"source":  strProp("Source name (optional for audience; required when metric is fleet)."),
			"format":  strProp("Package format filter (optional when metric is downloads, e.g. deb, rpm, appimage)."),
			"country": strProp("2-letter ISO country code (required when metric is country, e.g. US)."),
			"app":     appProp,
		}, "metric"),
		Annotations: readOnlyAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		q := url.Values{}
		setStr(q, "metric", args, "metric")
		setStr(q, "range", args, "range")
		setStr(q, "product", args, "product")
		setStr(q, "source", args, "source")
		setStr(q, "format", args, "format")
		setStr(q, "country", args, "country")
		setStr(q, "app", args, "app")
		return s.client.DoJSON(ctx, http.MethodGet, "/api/hearth/trend", q, nil)
	})

	s.addTool(Tool{
		Name:        "loot_reviews",
		Description: "Fetch app store rating summaries, per-app and per-store breakdowns, star distribution, daily rating trend, and written customer reviews.",
		InputSchema: schema(map[string]any{
			"range":  strProp("Window for reviews and rating trend (default: 90d).", "30d", "90d", "1y", "all"),
			"source": strProp("Optional store filter.", "appstore", "googleplay", "microsoftstore"),
			"stars":  strProp("Optional star filter (1..5, or 'low' for 1-2 star reviews).", "1", "2", "3", "4", "5", "low"),
			"limit":  intProp("Maximum number of written reviews to return (default: 200, max: 500)."),
			"app":    appProp,
		}),
		Annotations: readOnlyAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		q := url.Values{}
		setStr(q, "range", args, "range")
		setStr(q, "source", args, "source")
		setStr(q, "stars", args, "stars")
		setInt(q, "limit", args, "limit")
		setStr(q, "app", args, "app")
		return s.client.DoJSON(ctx, http.MethodGet, "/api/reviews", q, nil)
	})

	s.addTool(Tool{
		Name:        "loot_funnel",
		Description: "Fetch the acquisition funnel: ad spend, impressions, clicks, landing page views, conversions, link visits, site events, store installs, purchases, revenue, per-ad stats, reporting lag (as_of), and automated notes.",
		InputSchema: schema(map[string]any{
			"days": intProp("Number of trailing days to include, counting today (1-90, default: 14)."),
			"app":  appProp,
		}),
		Annotations: readOnlyAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		q := url.Values{}
		setInt(q, "days", args, "days")
		setStr(q, "app", args, "app")
		return s.client.DoJSON(ctx, http.MethodGet, "/api/funnel", q, nil)
	})

	s.addTool(Tool{
		Name:        "loot_recap",
		Description: "Fetch a monthly or full-year recap (revenue and units with period deltas, best day, new countries, top app/country/source, drops by rarity, achievements, and highlights).",
		InputSchema: schema(map[string]any{
			"month":  strProp("Month to summarize as YYYY-MM (omit both month and season for the last complete month)."),
			"season": strProp("Full calendar year to summarize as YYYY (e.g. 2026)."),
			"app":    appProp,
		}),
		Annotations: readOnlyAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		q := url.Values{}
		setStr(q, "month", args, "month")
		setStr(q, "season", args, "season")
		setStr(q, "app", args, "app")
		return s.client.DoJSON(ctx, http.MethodGet, "/api/recap", q, nil)
	})

	s.addTool(Tool{
		Name:        "loot_codex",
		Description: "Fetch lifetime totals, all-time records (best revenue/units/installs days, longest earning streak), and achievement trophy progress.",
		InputSchema: schema(map[string]any{
			"app": appProp,
		}),
		Annotations: readOnlyAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		q := url.Values{}
		setStr(q, "app", args, "app")
		return s.client.DoJSON(ctx, http.MethodGet, "/api/codex", q, nil)
	})

	s.addTool(Tool{
		Name:        "loot_drops",
		Description: "Fetch the paginated feed of recent drops/events (newest first; excludes drops still inside unopened daily chests).",
		InputSchema: schema(map[string]any{
			"limit":  intProp("Maximum drops to return (1-500, default: 100)."),
			"before": strProp("Pagination cursor: pass next_before from the previous page."),
			"app":    appProp,
		}),
		Annotations: readOnlyAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		q := url.Values{}
		setInt(q, "limit", args, "limit")
		setStr(q, "before", args, "before")
		setStr(q, "app", args, "app")
		return s.client.DoJSON(ctx, http.MethodGet, "/api/drops", q, nil)
	})

	s.addTool(Tool{
		Name:        "loot_stats",
		Description: "Fetch headline Loot stats: XP, totals by rarity and source, countries seen, waiting chests, open mysteries, active quests, alive bosses, and known apps.",
		InputSchema: schema(map[string]any{
			"app": appProp,
		}),
		Annotations: readOnlyAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		q := url.Values{}
		setStr(q, "app", args, "app")
		return s.client.DoJSON(ctx, http.MethodGet, "/api/stats", q, nil)
	})

	s.addTool(Tool{
		Name:        "loot_quests",
		Description: "Fetch active and recently completed weekly, monthly, and custom goals.",
		InputSchema: schema(map[string]any{
			"app": appProp,
		}),
		Annotations: readOnlyAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		q := url.Values{}
		setStr(q, "app", args, "app")
		return s.client.DoJSON(ctx, http.MethodGet, "/api/quests", q, nil)
	})

	s.addTool(Tool{
		Name:        "loot_mysteries",
		Description: "Fetch detected anomalies (traffic/install/refund spikes, quiet sources, new country clusters) and previously solved explanations.",
		InputSchema: schema(map[string]any{
			"app": appProp,
		}),
		Annotations: readOnlyAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		q := url.Values{}
		setStr(q, "app", args, "app")
		return s.client.DoJSON(ctx, http.MethodGet, "/api/mysteries", q, nil)
	})

	s.addTool(Tool{
		Name:        "loot_bosses",
		Description: "Fetch active and recently resolved crash-spike bosses from Play Vitals, Sentry, and Crashlytics relays.",
		InputSchema: schema(map[string]any{
			"app": appProp,
		}),
		Annotations: readOnlyAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		q := url.Values{}
		setStr(q, "app", args, "app")
		return s.client.DoJSON(ctx, http.MethodGet, "/api/bosses", q, nil)
	})

	s.addTool(Tool{
		Name:        "loot_sources",
		Description: "Fetch every configured data source's mode, poll interval, last poll time, last error, and total event count.",
		InputSchema: schema(map[string]any{}),
		Annotations: readOnlyAnno,
	}, func(ctx context.Context, _ map[string]any) (json.RawMessage, error) {
		return s.client.DoJSON(ctx, http.MethodGet, "/api/sources", nil, nil)
	})

	s.addTool(Tool{
		Name:        "loot_apps",
		Description: "Fetch canonical product mappings and any unmapped (source, app) pairs seen in the database.",
		InputSchema: schema(map[string]any{}),
		Annotations: readOnlyAnno,
	}, func(ctx context.Context, _ map[string]any) (json.RawMessage, error) {
		return s.client.DoJSON(ctx, http.MethodGet, "/api/apps", nil, nil)
	})

	s.addTool(Tool{
		Name:        "loot_chests",
		Description: "Fetch unopened daily chests waiting to be revealed.",
		InputSchema: schema(map[string]any{}),
		Annotations: readOnlyAnno,
	}, func(ctx context.Context, _ map[string]any) (json.RawMessage, error) {
		return s.client.DoJSON(ctx, http.MethodGet, "/api/chest", nil, nil)
	})

	if !s.allowWrites {
		return
	}

	s.addTool(Tool{
		Name:        "loot_chest_open",
		Description: "Open a waiting daily chest (the oldest by default, a specific YYYY-MM-DD date, or all waiting chests).",
		InputSchema: schema(map[string]any{
			"date": strProp("Specific chest date as YYYY-MM-DD (omit to open the oldest waiting chest)."),
			"all":  boolProp("Open every waiting chest, oldest first."),
		}),
		Annotations: writeAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		body := map[string]any{}
		if boolArg(args, "all") {
			body["all"] = true
		} else if d := strArg(args, "date"); d != "" {
			body["date"] = d
		}
		return s.client.DoJSON(ctx, http.MethodPost, "/api/chest/open", nil, body)
	})

	s.addTool(Tool{
		Name:        "loot_quest_create",
		Description: "Create a custom goal quest.",
		InputSchema: schema(map[string]any{
			"metric": strProp("Metric to track (e.g. revenue, units, installs, countries, xp, stars)."),
			"target": numProp("Numeric target value (> 0)."),
			"window": strProp("Goal window: 'week' or 'month'."),
			"app":    appProp,
			"source": strProp("Optional source filter."),
			"title":  strProp("Optional custom title."),
		}, "metric", "target", "window"),
		Annotations: writeAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		return s.client.DoJSON(ctx, http.MethodPost, "/api/quests", nil, args)
	})

	s.addTool(Tool{
		Name:        "loot_mystery_solve",
		Description: "Record an explanation for an open anomaly mystery and mark it solved.",
		InputSchema: schema(map[string]any{
			"id":   strProp("Mystery ID from loot_mysteries."),
			"note": strProp("Short explanation of what caused the anomaly."),
		}, "id"),
		Annotations: writeAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		id := strArg(args, "id")
		if id == "" {
			return nil, fmt.Errorf("id is required")
		}
		return s.client.DoJSON(ctx, http.MethodPost, "/api/mysteries/"+url.PathEscape(id)+"/solve", nil, map[string]any{
			"note": strArg(args, "note"),
		})
	})

	s.addTool(Tool{
		Name:        "loot_boss_slay",
		Description: "Mark an active crash-spike boss as slain.",
		InputSchema: schema(map[string]any{
			"id": strProp("Boss ID from loot_bosses."),
		}, "id"),
		Annotations: writeAnno,
	}, func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
		id := strArg(args, "id")
		if id == "" {
			return nil, fmt.Errorf("id is required")
		}
		return s.client.DoJSON(ctx, http.MethodPost, "/api/bosses/"+url.PathEscape(id)+"/slay", nil, map[string]any{})
	})
}

func strArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprint(v)
	}
	return strings.TrimSpace(s)
}

func boolArg(args map[string]any, key string) bool {
	v, ok := args[key]
	if !ok || v == nil {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

func setStr(q url.Values, param string, args map[string]any, key string) {
	if s := strArg(args, key); s != "" {
		q.Set(param, s)
	}
}

func setInt(q url.Values, param string, args map[string]any, key string) {
	v, ok := args[key]
	if !ok || v == nil {
		return
	}
	switch n := v.(type) {
	case float64:
		q.Set(param, strconv.Itoa(int(n)))
	case int:
		q.Set(param, strconv.Itoa(n))
	case string:
		if s := strings.TrimSpace(n); s != "" {
			q.Set(param, s)
		}
	}
}
