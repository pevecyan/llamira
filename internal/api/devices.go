package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"llamira/internal/gpu"
	"llamira/internal/llamacpp"
	"llamira/internal/system"
)

type deviceCache struct {
	binary  string
	at      time.Time
	devs    []llamacpp.Device
	running bool
}

// deviceNames returns llama.cpp's name for each GPU (CUDA0, ROCm0, Vulkan0, …), from
// `llama-server --list-devices` of the active binary (cached per binary). Until that is known,
// or when it does not line up with the GPUs, names follow the vendor: CUDA for NVIDIA, ROCm or
// Vulkan (per the installed llama.cpp flavor) for AMD. With wait=false it never blocks.
func (s *Server) deviceNames(ctx context.Context, gs []gpu.GPU, wait bool) []string {
	bin := s.ActiveBinary(ctx)
	s.devMu.Lock()
	c := s.dev
	stale := c.binary != bin || time.Since(c.at) > 6*time.Hour
	if stale && !c.running {
		s.dev.running = true
		done := make(chan struct{})
		go func() {
			defer close(done)
			env := s.Cfg.Router.Env
			if len(env) == 0 {
				env, _ = system.UnitEnv(context.Background(), s.Cfg.Router.Service)
			}
			devs, _ := llamacpp.ListDevices(context.Background(), bin, s.Cfg.Router.WorkDir, env)
			s.devMu.Lock()
			s.dev = deviceCache{binary: bin, at: time.Now(), devs: devs}
			s.devMu.Unlock()
		}()
		s.devMu.Unlock()
		if wait {
			select {
			case <-done:
			case <-ctx.Done():
			}
		}
		s.devMu.Lock()
		c = s.dev
	}
	s.devMu.Unlock()

	var devs []llamacpp.Device
	for _, d := range c.devs {
		if !strings.HasPrefix(d.Name, "CPU") && !strings.HasPrefix(d.Name, "BLAS") {
			devs = append(devs, d)
		}
	}
	names := make([]string, len(gs))
	for i, g := range gs {
		if len(devs) == len(gs) {
			names[i] = devs[i].Name
			continue
		}
		prefix := "CUDA"
		if g.Vendor == "amd" {
			prefix = "ROCm"
			if s.hostInfo().Flavor == "vulkan" {
				prefix = "Vulkan"
			}
		}
		names[i] = fmt.Sprintf("%s%d", prefix, g.Index)
	}
	return names
}

func (s *Server) hostInfo() llamacpp.Host {
	s.hostOnce.Do(func() { s.host = llamacpp.DetectHost(context.Background(), "/") })
	return s.host
}
