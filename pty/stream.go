package pty

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// streamEvent is the minimal shape of a claude --output-format stream-json line.
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Message struct {
		Content []contentBlock `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`  // tool_use block: tool name
	Input json.RawMessage `json:"input"` // tool_use block: tool arguments
}

// parseStream reads newline-delimited JSON from r, fires statusFn for each
// tool call seen, and returns the final response text from the result event.
// statusFn may be nil.
func parseStream(r io.Reader, statusFn func(string)) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4<<20), 4<<20) // 4 MB — verbose hook payloads can be large

	var (
		result   string
		lastLabel string
	)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "assistant":
			if statusFn == nil {
				continue
			}
			for _, block := range ev.Message.Content {
				if block.Type == "tool_use" && block.Name != "" {
					label := toolLabel(block.Name, block.Input)
					if label != lastLabel {
						lastLabel = label
						statusFn(label)
					}
				}
			}
		case "result":
			if ev.IsError {
				return "", fmt.Errorf("claude returned error result")
			}
			result = ev.Result
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	return result, nil
}

var toolEmoji = map[string]string{
	"Bash":      "🖥️",
	"Read":      "📄",
	"Write":     "✍️",
	"Edit":      "✏️",
	"WebFetch":  "🌐",
	"WebSearch": "🔍",
	"Glob":      "🔎",
	"Grep":      "🔎",
	"Skill":     "⚡",
	"TodoWrite": "📋",
	"Task":      "🤖",
}

// toolLabel builds a human-readable status string for a tool call, including
// the most relevant snippet from its input arguments.
func toolLabel(name string, input json.RawMessage) string {
	emoji, ok := toolEmoji[name]
	if !ok {
		emoji = "🔧"
	}
	detail := toolDetail(name, input)
	if detail == "" {
		return emoji + " " + name + "…"
	}
	return emoji + " " + name + ": " + detail
}

// toolDetail extracts a short human-readable snippet from a tool's input JSON.
func toolDetail(name string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil {
		return ""
	}
	// Per-tool: pick the most informative field.
	pick := func(keys ...string) string {
		for _, k := range keys {
			v, ok := args[k]
			if !ok {
				continue
			}
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				continue
			}
			s = strings.TrimSpace(s)
			if s != "" {
				return trunc(s, 60)
			}
		}
		return ""
	}
	switch name {
	case "Bash":
		// Prefer description (concise intent) over raw command (often long).
		return pick("description", "command")
	case "Read", "Write", "Edit":
		return pick("file_path")
	case "WebFetch":
		return pick("url")
	case "WebSearch":
		return pick("query")
	case "Glob":
		return pick("pattern")
	case "Grep":
		return pick("pattern", "include")
	case "Skill":
		return pick("skill")
	case "Task":
		return pick("description", "prompt")
	default:
		return ""
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
