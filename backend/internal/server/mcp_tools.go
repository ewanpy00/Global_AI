package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ewanpy00/Global_AI/backend/internal/mcp"
	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

const mcpInstructions = `Freshgate checks whether facts in a text are still current. Use check_freshness on any answer, report or draft that states prices, versions, laws, executives, company facts or statistics before you rely on it or send it to a user. Use correct_text to get the same text back with outdated statements rewritten from live sources. audit_site checks a whole website; compare_models asks several LLMs the same question and ranks them by how current their answers are.`

// MCP returns the Model Context Protocol server with Freshgate's tools. It is served at /mcp
// (Streamable HTTP) and by `freshgate -mcp` (stdio, for Claude Desktop and other local clients).
func (s *Server) MCP() *mcp.Server {
	m := mcp.NewServer("freshgate", "1.0.0", mcpInstructions)
	textSchema := func(desc string) map[string]any {
		return map[string]any{
			"type":     "object",
			"required": []string{"text"},
			"properties": map[string]any{
				"text": map[string]any{"type": "string", "description": desc},
			},
		}
	}
	m.AddTool(mcp.Tool{
		Name:        "check_freshness",
		Title:       "Check facts for staleness",
		Description: "Finds time-sensitive claims (prices, API/software versions, laws, leadership, company facts, statistics) in a text, re-checks each against live web sources (Tavily) and returns a 0-100 freshness score per claim and for the text, with the current value and a source for every outdated claim.",
		InputSchema: textSchema("The text to check, e.g. an AI-generated answer or a draft."),
		Handler: func(ctx context.Context, raw json.RawMessage) (string, any, error) {
			text, err := textArg(raw)
			if err != nil {
				return "", nil, err
			}
			rep, err := s.runForTool(ctx, text)
			if err != nil {
				return "", nil, err
			}
			return reportText(rep), map[string]any{"summary": rep.Summary, "claims": compact(rep)}, nil
		},
	})
	m.AddTool(mcp.Tool{
		Name:        "correct_text",
		Title:       "Rewrite outdated facts",
		Description: "Like check_freshness, but returns the text with every outdated or incorrect claim rewritten to today's value from live sources, plus the list of edits and their sources.",
		InputSchema: textSchema("The text to correct."),
		Handler: func(ctx context.Context, raw json.RawMessage) (string, any, error) {
			text, err := textArg(raw)
			if err != nil {
				return "", nil, err
			}
			rep, err := s.runForTool(ctx, text)
			if err != nil {
				return "", nil, err
			}
			fixed, edits := verify.ApplyFixes(rep)
			var b strings.Builder
			b.WriteString(fixed)
			if len(edits) > 0 {
				fmt.Fprintf(&b, "\n\n---\n%d correction(s):\n", len(edits))
				for _, e := range edits {
					fmt.Fprintf(&b, "- %q → %q", e.Old, e.New)
					if e.Source != "" {
						fmt.Fprintf(&b, " (%s)", e.Source)
					}
					b.WriteString("\n")
				}
			} else {
				b.WriteString("\n\n---\nNo corrections needed.")
			}
			return b.String(), map[string]any{"corrected_text": fixed, "edits": edits, "summary": rep.Summary}, nil
		},
	})
	m.AddTool(mcp.Tool{
		Name:        "audit_site",
		Title:       "Audit a website for stale facts",
		Description: "Crawls a website with Tavily (same domain, up to max_pages pages), checks every page and ranks the pages by how many outdated facts they contain.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"url"},
			"properties": map[string]any{
				"url":       map[string]any{"type": "string", "description": "Start URL, e.g. https://docs.example.com"},
				"max_pages": map[string]any{"type": "integer", "minimum": 1, "maximum": maxAuditPages, "default": defaultAuditPages},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, any, error) {
			var a struct {
				URL      string `json:"url"`
				MaxPages int    `json:"max_pages"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", nil, errors.New("arguments must be {\"url\": ..., \"max_pages\": n}")
			}
			site, err := normalizeSiteURL(a.URL)
			if err != nil {
				return "", nil, err
			}
			ctx, cancel := context.WithTimeout(ctx, 4*s.Cfg.VerifyTimeout)
			defer cancel()
			res, err := s.RunAudit(ctx, site, a.MaxPages, nil)
			if err != nil {
				return "", nil, err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Site freshness %d/100 (%s): %d stale of %d claims on %d pages.\n", res.Score, res.Label, res.Stale, res.Claims, len(res.Pages))
			for _, p := range res.Pages {
				if p.Stale == 0 {
					continue
				}
				fmt.Fprintf(&b, "\n%s (%d stale)\n", p.URL, p.Stale)
				for _, c := range p.Items {
					if c.Verdict == "outdated" || c.Verdict == "incorrect" {
						fmt.Fprintf(&b, "- %q → now: %s\n", c.Text, orDash(c.CurrentValue))
					}
				}
			}
			return b.String(), res, nil
		},
	})
	m.AddTool(mcp.Tool{
		Name:        "compare_models",
		Title:       "Staleness Arena",
		Description: "Asks several LLMs (via Bifrost) the same question, fact-checks every answer against live sources and ranks the models by how current their facts are.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"question"},
			"properties": map[string]any{
				"question": map[string]any{"type": "string"},
				"models":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "provider/model ids; default: the configured arena models"},
			},
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, any, error) {
			var a arenaRequest
			if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Question) == "" {
				return "", nil, errors.New("arguments must be {\"question\": ..., \"models\": [...]}")
			}
			models, err := s.pickArenaModels(a.Models)
			if err != nil {
				return "", nil, err
			}
			ctx, cancel := context.WithTimeout(ctx, 2*s.Cfg.VerifyTimeout)
			defer cancel()
			board := s.RunArena(ctx, a.Question, models, nil)
			var b strings.Builder
			for _, e := range board {
				if e.Error != "" {
					fmt.Fprintf(&b, "%d. %s: failed (%s)\n", e.Rank, e.Model, e.Error)
					continue
				}
				fmt.Fprintf(&b, "%d. %s: %d/100 (%s), %d stale of %d claims\n", e.Rank, e.Model, e.Score, e.Label, e.Stale, e.Claims)
			}
			return b.String(), map[string]any{"leaderboard": board}, nil
		},
	})
	return m
}

func textArg(raw json.RawMessage) (string, error) {
	var a struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Text) == "" {
		return "", errors.New("argument \"text\" is required")
	}
	return a.Text, nil
}

func (s *Server) runForTool(ctx context.Context, text string) (*verify.Report, error) {
	ctx, cancel := context.WithTimeout(ctx, s.Cfg.VerifyTimeout)
	defer cancel()
	return s.Engine.Run(ctx, text, nil)
}

// reportText is the human-readable tool result: score, then one line per claim that needs attention.
func reportText(r *verify.Report) string {
	var b strings.Builder
	s := r.Summary
	if s.Total == 0 {
		return "No time-sensitive claims found."
	}
	fmt.Fprintf(&b, "Freshness %d/100 (%s). %d claims: ", s.Score, s.Label, s.Total)
	var parts []string
	for _, v := range []verify.Verdict{verify.VerdictOutdated, verify.VerdictIncorrect, verify.VerdictUnverifiable, verify.VerdictCurrent} {
		if n := s.ByVerdict[v]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, v))
		}
	}
	b.WriteString(strings.Join(parts, ", ") + ".\n")
	for _, c := range compact(r) {
		mark := "✓"
		switch c.Verdict {
		case "outdated", "incorrect":
			mark = "✗"
		case "unverifiable":
			mark = "?"
		}
		fmt.Fprintf(&b, "\n%s [%d] %q: %s", mark, c.Score, c.Text, c.Verdict)
		if c.CurrentValue != "" && c.Verdict != "current" {
			fmt.Fprintf(&b, ". Now: %s", c.CurrentValue)
		}
		if c.Source != "" {
			fmt.Fprintf(&b, " (%s)", c.Source)
		}
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}
