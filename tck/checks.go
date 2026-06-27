package tck

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/elek/acpp/acp"
)

// checkAgentCapabilities reports what the agent advertised in its initialize
// response: protocol version, agent info, and every declared capability flag
// (flattened from the agentCapabilities blob to dotted paths).
func checkAgentCapabilities(t *Transcript) []Result {
	var out []Result

	out = append(out, Result{
		Name:  "protocol version",
		OK:    true,
		Value: fmt.Sprintf("%v", t.Init.ProtocolVersion),
	})

	if info := t.Init.AgentInfo; info != nil {
		out = append(out, Result{
			Name:  "agent info",
			OK:    true,
			Value: strings.TrimSpace(info.Name + " " + info.Version),
		})
	}

	caps := flattenCaps(t.Init.AgentCapabilities)
	if len(caps) == 0 {
		out = append(out, Result{Name: "agent capabilities", OK: false, Value: "none declared"})
		return out
	}
	out = append(out, caps...)
	return out
}

// flattenCaps parses the agentCapabilities JSON blob and flattens it into one
// result per leaf, named "cap: <dotted.path>". Boolean leaves carry their truth
// as OK; non-boolean leaves are reported as declared (OK=true).
func flattenCaps(raw json.RawMessage) []Result {
	if len(raw) == 0 {
		return nil
	}
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return []Result{{Name: "agent capabilities", OK: false, Value: "unparseable: " + err.Error()}}
	}
	var out []Result
	flattenInto(&out, "", top)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func flattenInto(out *[]Result, prefix string, v any) {
	switch val := v.(type) {
	case map[string]any:
		for k, sub := range val {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			flattenInto(out, p, sub)
		}
	case bool:
		*out = append(*out, Result{Name: "cap: " + prefix, OK: val, Value: fmt.Sprintf("%v", val)})
	case []any:
		*out = append(*out, Result{Name: "cap: " + prefix, OK: len(val) > 0, Value: fmt.Sprintf("%d item(s)", len(val))})
	default:
		*out = append(*out, Result{Name: "cap: " + prefix, OK: val != nil, Value: fmt.Sprintf("%v", val)})
	}
}

// checkAvailableCommands reports the slash commands the agent announced.
func checkAvailableCommands(t *Transcript) []Result {
	names := make([]string, 0, len(t.Commands))
	for _, c := range t.Commands {
		names = append(names, c.Name)
	}
	value := fmt.Sprintf("%d", len(names))
	if len(names) > 0 {
		value = fmt.Sprintf("%d: %s", len(names), truncate(strings.Join(names, ", "), 60))
	}
	return []Result{{Name: "available commands", OK: len(names) > 0, Value: value}}
}

// checkUsage inspects usage_update notifications: whether any arrived, whether
// cost was reported, and whether context size/used was reported.
func checkUsage(t *Transcript) []Result {
	var all []acp.SessionUsageUpdate
	for _, tag := range t.Order {
		all = append(all, t.Turns[tag].Usage...)
	}

	received := Result{Name: "usage_update received", OK: len(all) > 0, Value: fmt.Sprintf("%d update(s)", len(all))}

	cost := Result{Name: "usage cost reported", OK: false, Value: "—"}
	for _, u := range all {
		if u.Cost != nil {
			cost.OK = true
			cost.Value = fmt.Sprintf("%g %s", u.Cost.Amount, u.Cost.Currency)
			break
		}
	}

	sizeUsed := Result{Name: "usage size/used reported", OK: false, Value: "—"}
	for _, u := range all {
		if u.Size > 0 || u.Used > 0 {
			sizeUsed.OK = true
			sizeUsed.Value = fmt.Sprintf("%d/%d", u.Used, u.Size)
			break
		}
	}

	return []Result{received, cost, sizeUsed}
}

// checkMeta reports every distinct _meta key seen anywhere in the stream.
func checkMeta(t *Transcript) []Result {
	keys := make([]string, 0, len(t.MetaKeys))
	for k := range t.MetaKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	value := "none"
	if len(keys) > 0 {
		value = truncate(strings.Join(keys, ", "), 80)
	}
	return []Result{{Name: "custom _meta", OK: len(keys) > 0, Value: value}}
}

// checkPromptFinished verifies every probe turn was closed by a PromptResponse
// and reports the stop reasons.
func checkPromptFinished(t *Transcript) []Result {
	if len(t.Order) == 0 {
		return []Result{{Name: "prompt finished", OK: false, Value: "no turns"}}
	}
	ok := true
	var parts []string
	for _, tag := range t.Order {
		turn := t.Turns[tag]
		if !turn.GotResponse {
			ok = false
			parts = append(parts, tag+"=<none>")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", tag, turn.StopReason))
	}
	return []Result{{Name: "prompt finished", OK: ok, Value: strings.Join(parts, " ")}}
}

// checkToolUsage reports whether any tool_call updates were observed.
func checkToolUsage(t *Transcript) []Result {
	count := 0
	kinds := map[string]bool{}
	for _, tag := range t.Order {
		for _, tc := range t.Turns[tag].ToolCalls {
			count++
			if tc.Kind != "" {
				kinds[string(tc.Kind)] = true
			}
		}
	}
	value := fmt.Sprintf("%d", count)
	if len(kinds) > 0 {
		ks := make([]string, 0, len(kinds))
		for k := range kinds {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		value = fmt.Sprintf("%d (%s)", count, strings.Join(ks, ", "))
	}
	return []Result{{Name: "tool usage events", OK: count > 0, Value: value}}
}

// checkCapital is the simple-conversation probe: the answer should mention Madrid.
func checkCapital(t *Transcript) []Result {
	turn := t.Turns["capital"]
	if turn == nil {
		return []Result{{Name: "conversation: capital", OK: false, Value: "not run"}}
	}
	ok := strings.Contains(strings.ToLower(turn.Text), "madrid")
	return []Result{{Name: "conversation: capital", OK: ok, Value: truncate(snippet(turn.Text), 60)}}
}

// checkListDir is the tool-usage probe: the answer should name the unique probe
// file pre-created in the working directory.
func checkListDir(t *Transcript) []Result {
	turn := t.Turns["list-dir"]
	if turn == nil {
		return []Result{{Name: "conversation: list-dir", OK: false, Value: "not run"}}
	}
	ok := t.ProbeFile != "" && strings.Contains(turn.Text, t.ProbeFile)
	value := truncate(snippet(turn.Text), 60)
	if ok {
		value = t.ProbeFile
	}
	return []Result{{Name: "conversation: list-dir", OK: ok, Value: value}}
}

// snippet collapses whitespace in s into a single line.
func snippet(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncate shortens s to at most n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
