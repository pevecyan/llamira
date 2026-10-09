package api

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// routerProxy forwards a request to one of the router's OpenAI-compatible endpoints
// (streaming passes through unbuffered). The UI playground uses it for chat and embeddings,
// so it also works when the UI is reached through an SSH tunnel.
func (s *Server) routerProxy(path string) http.Handler {
	target, _ := url.Parse(s.Cfg.Router.URL)
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path = path
			r.Out.URL.RawPath = ""
			r.Out.Host = target.Host
			r.Out.Header.Del("Cookie")
			r.Out.Header.Del("Authorization") // the UI's own token is not the router's key
			if k := s.Router.Key(); k != "" {
				r.Out.Header.Set("Authorization", "Bearer "+k)
			}
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeErr(w, http.StatusBadGateway, "router: %v", err)
		},
	}
}
