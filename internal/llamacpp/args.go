package llamacpp

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Arg describes one llama-server command-line argument, as parsed from --help.
type Arg struct {
	Key         string   `json:"key"`               // canonical INI key: last positive name without dashes (matches llama.cpp to_ini)
	Names       []string `json:"names"`             // positive names without dashes
	NegNames    []string `json:"neg_names"`         // negation names without dashes (e.g. no-kv-offload)
	Env         string   `json:"env,omitempty"`     // LLAMA_ARG_* env name, if any
	Value       string   `json:"value,omitempty"`   // value placeholder (empty = boolean flag)
	Allowed     []string `json:"allowed,omitempty"` // allowed values, if listed
	Description string   `json:"description"`
	Group       string   `json:"group"`
}

// IsFlag reports whether the argument takes no value (boolean on/off).
func (a *Arg) IsFlag() bool { return a.Value == "" }

// Preset-only keys accepted by the router's preset parser that are not CLI arguments.
var PresetOnlyKeys = map[string]string{
	"load-on-startup":    "Load this model when the router starts (bool)",
	"stop-timeout":       "Seconds to wait after unload before forcing termination (int)",
	"dedup-cache-models": "Hide cached model entries that a preset already references via hf-repo (bool)",
}

// RouterControlledKeys are set by the router itself and removed/overwritten on load.
// ("alias" is not one of them: in a preset it defines extra names the router routes to the
// model; the router only passes the canonical name to the child process.)
var RouterControlledKeys = map[string]bool{
	"host": true, "port": true, "api-key": true,
}

// Catalog is the set of arguments supported by an installed llama-server binary.
type Catalog struct {
	Version string `json:"version"`
	Args    []*Arg `json:"args"`
	byName  map[string]*Arg
}

var (
	reGroup   = regexp.MustCompile(`^-{3,} (.+?) -{3,}$`)
	reEnv     = regexp.MustCompile(`\(env: ([A-Z0-9_]+)\)`)
	reAllowed = regexp.MustCompile(`allowed values: ([a-z0-9_]+(?:, [a-z0-9_]+)*)`)
)

// descColumn is where llama-server --help starts the description column.
const descColumn = 40

// ParseHelp parses the output of `llama-server --help`.
func ParseHelp(help string) *Catalog {
	c := &Catalog{}
	var cur *Arg
	var desc []string
	group := ""
	flush := func() {
		if cur == nil {
			return
		}
		full := strings.Join(desc, " ")
		if m := reEnv.FindStringSubmatch(full); m != nil {
			cur.Env = m[1]
		}
		if m := reAllowed.FindStringSubmatch(full); m != nil {
			cur.Allowed = strings.Split(m[1], ", ")
		}
		cur.Description = strings.TrimSpace(reEnv.ReplaceAllString(full, ""))
		c.Args = append(c.Args, cur)
		cur, desc = nil, nil
	}

	sc := bufio.NewScanner(strings.NewReader(help))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if m := reGroup.FindStringSubmatch(line); m != nil {
			flush()
			group = m[1]
			continue
		}
		if strings.HasPrefix(line, "-") {
			flush()
			head, rest := line, ""
			if len(line) > descColumn && strings.TrimSpace(line[descColumn-2:descColumn]) == "" {
				head, rest = line[:descColumn], line[descColumn:]
			}
			cur = parseArgHead(strings.TrimSpace(head))
			cur.Group = group
			if r := strings.TrimSpace(rest); r != "" {
				desc = append(desc, r)
			}
			continue
		}
		if cur != nil && strings.TrimSpace(line) != "" {
			desc = append(desc, strings.TrimSpace(line))
		}
	}
	flush()
	c.index()
	return c
}

// parseArgHead parses "-ngl,  --gpu-layers, --n-gpu-layers N" into names and value placeholder.
func parseArgHead(head string) *Arg {
	a := &Arg{}
	// Names are comma-separated; the value placeholder (which may itself contain commas,
	// e.g. "<dev1,dev2,..>") follows the last name without a trailing comma.
	var names []string
	fields := strings.Fields(head)
	for i, f := range fields {
		if i == 0 || (strings.HasSuffix(fields[i-1], ",") && strings.HasPrefix(f, "-")) {
			names = append(names, strings.TrimSuffix(f, ","))
			continue
		}
		a.Value = strings.Join(fields[i:], " ")
		break
	}
	// Positive names come first, negations after. The negation group starts after the
	// last positive long name that precedes the first "--no-" name.
	firstNeg := -1
	for i, n := range names {
		if strings.HasPrefix(n, "--no-") {
			firstNeg = i
			break
		}
	}
	split := len(names)
	if firstNeg >= 0 {
		lastPosLong := -1
		for i := 0; i < firstNeg; i++ {
			if strings.HasPrefix(names[i], "--") {
				lastPosLong = i
			}
		}
		if lastPosLong >= 0 {
			split = lastPosLong + 1
		}
	}
	for i, n := range names {
		n = strings.TrimLeft(n, "-")
		if i < split {
			a.Names = append(a.Names, n)
		} else {
			a.NegNames = append(a.NegNames, n)
		}
	}
	if len(a.Names) > 0 {
		a.Key = a.Names[len(a.Names)-1]
	}
	return a
}

func (c *Catalog) index() {
	c.byName = map[string]*Arg{}
	for _, a := range c.Args {
		for _, n := range a.Names {
			c.byName[n] = a
		}
		for _, n := range a.NegNames {
			c.byName[n] = a
		}
		if a.Env != "" {
			c.byName[a.Env] = a
		}
	}
}

// Lookup resolves an INI key (long/short name, negation or env name) to its argument.
func (c *Catalog) Lookup(key string) (*Arg, bool) {
	a, ok := c.byName[key]
	return a, ok
}

// Canonical maps a key to its canonical INI key. Unknown keys are returned unchanged.
func (c *Catalog) Canonical(key string) string {
	if a, ok := c.byName[key]; ok {
		return a.Key
	}
	return key
}

// Known reports whether a key is accepted in a preset section.
func (c *Catalog) Known(key string) bool {
	if _, ok := PresetOnlyKeys[key]; ok {
		return true
	}
	_, ok := c.byName[key]
	return ok
}

// Keys returns all canonical keys, sorted.
func (c *Catalog) Keys() []string {
	var keys []string
	for _, a := range c.Args {
		keys = append(keys, a.Key)
	}
	sort.Strings(keys)
	return keys
}

// CatalogCache loads the catalog from the installed binary and caches it per binary path.
type CatalogCache struct {
	Binary  string
	Resolve func(context.Context) string // optional: current binary (e.g. after an update)
	mu      sync.Mutex
	cat     *Catalog
	catBin  string
}

func (cc *CatalogCache) Get(ctx context.Context) (*Catalog, error) {
	bin := cc.Binary
	if cc.Resolve != nil {
		if b := cc.Resolve(ctx); b != "" {
			bin = b
		}
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.cat != nil && cc.catBin == bin {
		return cc.cat, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--help").CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("run %s --help: %w", bin, err)
	}
	cat := ParseHelp(string(out))
	if len(cat.Args) < 20 {
		return nil, fmt.Errorf("could not parse %s --help (%d args found)", bin, len(cat.Args))
	}
	if v, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput(); err == nil {
		for _, l := range strings.Split(string(v), "\n") {
			if strings.HasPrefix(l, "version:") {
				cat.Version = strings.TrimSpace(strings.TrimPrefix(l, "version:"))
			}
		}
	}
	cc.cat, cc.catBin = cat, bin
	return cat, nil
}

// Invalidate forces the catalog to be re-read (e.g. after llama.cpp was rebuilt).
func (cc *CatalogCache) Invalidate() {
	cc.mu.Lock()
	cc.cat = nil
	cc.mu.Unlock()
}
