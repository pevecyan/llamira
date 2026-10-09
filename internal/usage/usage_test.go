package usage

import (
	"testing"
	"time"
)

func TestParser(t *testing.T) {
	p := NewParser()
	now := time.Now()
	lines := []string{
		"559.14.106.843 I srv  proxy_reques: proxying request to model lmstudio/Qwen:Q4_K_M on port 60611",
		"[60611] 0.29.1 I slot print_timing: id  3 | task 0 | n_gen =   1763, tg =  41.79 t/s, tg_3s =  41.54 t/s",
		"[60611] 0.29.601.822 I slot print_timing: id  3 | task 0 | prompt eval time =     112.93 ms /    16 tokens (    7.06 ms per token,   141.68 tokens per second)",
		"[60611] 0.29.601.831 I slot print_timing: id  3 | task 0 |        eval time =    2302.17 ms /   123 tokens (   18.87 ms per token,    52.99 tokens per second)",
		"[60611] 0.29.601.832 I slot print_timing: id  3 | task 0 |       total time =    2415.10 ms /   139 tokens",
		"[60611] 0.29.601.838 I slot print_timing: id  3 | task 0 |    graphs reused =        122",
	}
	var got []*Request
	for _, l := range lines {
		if r := p.Line(now, l); r != nil {
			got = append(got, r)
		}
	}
	if len(got) != 1 {
		t.Fatalf("got %d requests", len(got))
	}
	r := got[0]
	if r.Model != "lmstudio/Qwen:Q4_K_M" || r.PromptN != 16 || r.GenN != 123 || r.GenMS != 2302.17 || r.PromptMS != 112.93 {
		t.Errorf("got %+v", r)
	}
}
