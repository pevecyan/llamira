package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"llamira/internal/gguf"
)

// cmdInspect prints GGUF metadata and a per-layer size summary of a model file.
func cmdInspect(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: inspect FILE.gguf")
	}
	info, tensors, err := gguf.ReadTensors(args[0])
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(info, "", "  ")
	fmt.Println(string(b))
	layers := map[string]int64{}
	other := map[string]int64{}
	var total int64
	for _, t := range tensors {
		total += t.Size
		if rest, ok := strings.CutPrefix(t.Name, "blk."); ok {
			n, _, _ := strings.Cut(rest, ".")
			layers[n] += t.Size
		} else {
			other[t.Name] += t.Size
		}
	}
	fmt.Printf("\ntensors: %d, %.2f GiB in %d layers\n", len(tensors), gib(total), len(layers))
	var names []string
	for n := range other {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("  %-28s %8.1f MiB\n", n, float64(other[n])/(1<<20))
	}
	var keys []string
	for k := range layers {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return atoiSafe(keys[i]) < atoiSafe(keys[j]) })
	for _, k := range keys {
		fmt.Printf("  blk.%-3s %8.1f MiB\n", k, float64(layers[k])/(1<<20))
	}
	return nil
}

func gib(n int64) float64 { return float64(n) / (1 << 30) }

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}
