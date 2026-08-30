package config

import (
	"sort"
	"strings"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

// HookConfig configures a single hook. Type selects the registered hook
// implementation; Params carries every other key as a string, delivered verbatim
// to the hook factory. Global config hooks are decoded from YAML (see
// UnmarshalYAML); per-project hooks are parsed from the compact string syntax
// stored in the project's `hooks` column (see ParseHookList).
type HookConfig struct {
	Type   string
	Params map[string]string
}

// UnmarshalYAML reads a hook entry as a flat map: the `type` key becomes Type,
// and every remaining key is folded into Params as a string. This lets hooks
// declare arbitrary params without a fixed schema, e.g.
//
//	- type: qdrant
//	  url: http://localhost:6333
//	  collection: acpp
func (h *HookConfig) UnmarshalYAML(value *yaml.Node) error {
	var raw map[string]string
	if err := value.Decode(&raw); err != nil {
		return errors.Wrap(err, "decoding hook config")
	}
	h.Type = raw["type"]
	if h.Type == "" {
		return errors.New("hook config missing required 'type' field")
	}
	delete(raw, "type")
	h.Params = raw
	return nil
}

// ParseHookList parses the compact hook syntax stored in a project's `hooks`
// column into the same HookConfig shape the global config uses, so both sources
// feed hook.Build unchanged.
//
// The syntax is a comma-separated list of entries, each either a bare hook type
// or a type followed by ":" and semicolon-separated key=value params:
//
//	commit
//	commit,worktree
//	worktree:location=/tmp/wt
//	commit,worktree:location=/tmp/wt;mode=fast
//
// Only the first "=" in a param separates key from value, so values may contain
// "=". Empty entries are skipped, which makes a trailing comma harmless.
func ParseHookList(s string) ([]HookConfig, error) {
	var cfgs []HookConfig
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		typ, paramStr, hasParams := strings.Cut(entry, ":")
		typ = strings.TrimSpace(typ)
		if typ == "" {
			return nil, errors.Errorf("missing hook type in entry %q", entry)
		}

		params := map[string]string{}
		if hasParams {
			for _, kv := range strings.Split(paramStr, ";") {
				kv = strings.TrimSpace(kv)
				if kv == "" {
					continue
				}
				key, value, ok := strings.Cut(kv, "=")
				key = strings.TrimSpace(key)
				if !ok {
					return nil, errors.Errorf("hook %q: param %q is not key=value", typ, kv)
				}
				if key == "" {
					return nil, errors.Errorf("hook %q: param %q has an empty key", typ, kv)
				}
				params[key] = value
			}
		}
		cfgs = append(cfgs, HookConfig{Type: typ, Params: params})
	}
	return cfgs, nil
}

// FormatHookList renders hook configs back into the compact syntax ParseHookList
// accepts. Params are sorted so the output is stable across map iterations.
func FormatHookList(cfgs []HookConfig) string {
	entries := make([]string, 0, len(cfgs))
	for _, c := range cfgs {
		if len(c.Params) == 0 {
			entries = append(entries, c.Type)
			continue
		}
		keys := make([]string, 0, len(c.Params))
		for k := range c.Params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+c.Params[k])
		}
		entries = append(entries, c.Type+":"+strings.Join(parts, ";"))
	}
	return strings.Join(entries, ",")
}
