package doctor

import (
	"errors"
	"strings"
	"testing"
)

func byID(cs []Check) map[string]Check {
	m := map[string]Check{}
	for _, c := range cs {
		m[c.ID] = c
	}
	return m
}

func healthy() Input {
	return Input{
		PresetFile: "/var/lib/llamira/models.ini", Service: "llama-server.service", RouterRole: "router",
		ExecStart: strings.Fields("/opt/llama.cpp/build/bin/llama-server --host 0.0.0.0 --port 8080 --models-dir /models " +
			"--models-max 1 --models-preset /var/lib/llamira/models.ini --api-key-file /etc/llamira/router-api-key --sleep-idle-seconds -1"),
		UnitEnv:  []string{"LLAMA_CACHE=/models/cache", "LD_LIBRARY_PATH=/usr/local/cuda/lib64"},
		DiskPath: "/models", DiskFree: 500 << 30, DiskLowGB: 20, GPUCount: 2,
		Listen: "127.0.0.1:7070",
	}
}

func TestHealthyHost(t *testing.T) {
	cs := Run(healthy())
	for _, c := range cs {
		if c.Status != OK {
			t.Errorf("%s: %s (%s)", c.ID, c.Status, c.Detail)
		}
	}
	if Worst(cs) != OK {
		t.Error("worst should be ok")
	}
}

func TestFlags(t *testing.T) {
	f := Flags(strings.Fields("bin --host 0.0.0.0 -c 4096 --no-models-autoload --sleep-idle-seconds -1 --tensor-split=1,1"))
	want := map[string]string{"host": "0.0.0.0", "c": "4096", "no-models-autoload": "", "sleep-idle-seconds": "-1", "tensor-split": "1,1"}
	if len(f) != len(want) {
		t.Fatalf("got %v", f)
	}
	for k, v := range want {
		if f[k] != v {
			t.Errorf("%s = %q, want %q", k, f[k], v)
		}
	}
}

func TestCommandLineProblems(t *testing.T) {
	in := healthy()
	in.ExecStart = strings.Fields("/bin/llama-server --host 0.0.0.0 --port 8080 --models-dir /models -c 65536 -ngl 999 --split-mode layer")
	short := map[string]string{"c": "ctx-size", "ngl": "n-gpu-layers"}
	in.Canonical = func(s string) string {
		if c, ok := short[s]; ok {
			return c
		}
		return s
	}
	m := byID(Run(in))
	if m["preset-flag"].Status != Fail {
		t.Errorf("missing --models-preset should fail: %+v", m["preset-flag"])
	}
	ov := m["cli-overrides"]
	if ov.Status != Warn || !strings.Contains(ov.Detail, "--c ") || !strings.Contains(ov.Detail, "--ngl") || !strings.Contains(ov.Detail, "--split-mode") {
		t.Errorf("overrides: %+v", ov)
	}
	if m["router-key"].Status != Warn {
		t.Errorf("open router should warn: %+v", m["router-key"])
	}
	if Worst(Run(in)) != Fail {
		t.Error("worst should be fail")
	}

	in.ExecStart = strings.Fields("/bin/llama-server --models-preset /tmp/other.ini")
	if c := byID(Run(in))["preset-flag"]; c.Status != Fail || !strings.Contains(c.Detail, "/tmp/other.ini") {
		t.Errorf("wrong preset path: %+v", c)
	}
	if c := byID(Run(in))["router-key"]; c.Status != OK {
		t.Errorf("default host is loopback: %+v", c)
	}
}

func TestEnvironment(t *testing.T) {
	in := healthy()
	in.HFToken = true
	in.CacheDir = "/srv/cache"
	m := byID(Run(in))
	if m["hf-token"].Status != Warn {
		t.Errorf("hf token: %+v", m["hf-token"])
	}
	if m["cache-dir"].Status != Warn || !strings.Contains(m["cache-dir"].Detail, "/srv/cache") {
		t.Errorf("cache dir: %+v", m["cache-dir"])
	}
	in.UnitEnv = []string{"HF_TOKEN=x"}
	in.CacheDir = ""
	m = byID(Run(in))
	if m["hf-token"].Status != OK || m["cache-dir"].Status != Warn {
		t.Errorf("hf=%+v cache=%+v", m["hf-token"], m["cache-dir"])
	}
}

func TestResourcesAndUI(t *testing.T) {
	in := healthy()
	in.RouterErr = errors.New("connection refused")
	in.DiskFree = 5 << 30
	in.GPUErr = errors.New("nvidia-smi: not found")
	in.Issues = []Issue{{Severity: "error", Section: "x", Message: "unknown option"}}
	in.FailedIDs = []string{"a/b:Q4"}
	in.Listen = "0.0.0.0:7070"
	in.ListenAuth = true
	m := byID(Run(in))
	for id, want := range map[string]string{"router": Fail, "disk": Warn, "gpus": Warn, "preset-file": Fail, "models-failed": Warn, "ui-auth": Warn} {
		if m[id].Status != want {
			t.Errorf("%s = %s, want %s (%s)", id, m[id].Status, want, m[id].Detail)
		}
	}
	in.TLS = true
	if c := byID(Run(in))["ui-auth"]; c.Status != OK {
		t.Errorf("token+tls: %+v", c)
	}
	in.AllowUnauthenticated = true
	if c := byID(Run(in))["ui-auth"]; c.Status != Warn || !strings.Contains(c.Detail, "no authentication") {
		t.Errorf("no auth: %+v", c)
	}
}
