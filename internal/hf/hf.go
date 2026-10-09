// Package hf searches the Hugging Face Hub for GGUF models and groups repo files into the
// quantizations that llama.cpp's "org/repo:TAG" syntax can download.
package hf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"llamira/internal/gguf"
)

const BaseURL = "https://huggingface.co"

type Client struct {
	Token string
	HTTP  *http.Client
}

func NewClient(token string) *Client {
	return &Client{Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) get(ctx context.Context, p string, q url.Values, out any) error {
	u := BaseURL + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("huggingface.co: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errors.New("repository not found on huggingface.co")
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return errors.New("access denied by huggingface.co (gated or private repo: configure hf_token)")
	case resp.StatusCode >= 300:
		return fmt.Errorf("huggingface.co: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

// Repo is a search result.
type Repo struct {
	ID          string    `json:"id"`
	Downloads   int64     `json:"downloads"`
	Likes       int64     `json:"likes"`
	PipelineTag string    `json:"pipeline_tag,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	Tags        []string  `json:"tags"`
}

// Search finds GGUF repositories, most downloaded first.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Repo, error) {
	if limit <= 0 || limit > 100 {
		limit = 40
	}
	q := url.Values{
		"search": {query}, "filter": {"gguf"}, "sort": {"downloads"},
		"direction": {"-1"}, "limit": {fmt.Sprint(limit)},
	}
	var raw []struct {
		ID          string    `json:"id"`
		Downloads   int64     `json:"downloads"`
		Likes       int64     `json:"likes"`
		PipelineTag string    `json:"pipeline_tag"`
		CreatedAt   time.Time `json:"createdAt"`
		Tags        []string  `json:"tags"`
	}
	if err := c.get(ctx, "/api/models", q, &raw); err != nil {
		return nil, err
	}
	out := make([]Repo, 0, len(raw))
	for _, r := range raw {
		out = append(out, Repo{r.ID, r.Downloads, r.Likes, r.PipelineTag, r.CreatedAt, usefulTags(r.Tags)})
	}
	return out, nil
}

// usefulTags drops noisy hub tags (regions, base_model:..., endpoints...).
func usefulTags(tags []string) []string {
	out := []string{}
	for _, t := range tags {
		if strings.Contains(t, ":") || t == "gguf" || t == "endpoints_compatible" || t == "conversational" ||
			t == "region:us" || len(t) > 24 {
			continue
		}
		out = append(out, t)
		if len(out) == 8 {
			break
		}
	}
	return out
}

// File is one file in a repository.
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// Quant is a downloadable quantization: one GGUF, or all shards of a split GGUF.
type Quant struct {
	Tag        string   `json:"tag"`        // e.g. Q4_K_M (what goes after "repo:")
	Ref        string   `json:"ref"`        // org/repo:TAG
	Files      []File   `json:"files"`      // model file(s)
	Size       int64    `json:"size"`       // model size
	ExtraSize  int64    `json:"extra_size"` // size of sidecar files the router fetches too (mmproj, MTP head)
	Extras     []string `json:"extras"`     // those sidecar files
	Bits       int      `json:"bits"`       // first number in the tag (4 for Q4_K_M)
	Ambiguous  bool     `json:"ambiguous"`  // another file has the same tag; the router picks the first one
	Selectable bool     `json:"selectable"` // the router's tag lookup resolves to this quant
	Dir        string   `json:"dir,omitempty"`
}

// Detail is a repository with its quantizations.
type Detail struct {
	ID            string    `json:"id"`
	Author        string    `json:"author"`
	Downloads     int64     `json:"downloads"`
	Likes         int64     `json:"likes"`
	LastModified  time.Time `json:"last_modified"`
	Gated         bool      `json:"gated"`
	License       string    `json:"license,omitempty"`
	PipelineTag   string    `json:"pipeline_tag,omitempty"`
	BaseModel     string    `json:"base_model,omitempty"`
	Params        int64     `json:"params,omitempty"`
	Architecture  string    `json:"architecture,omitempty"`
	ContextLength int64     `json:"context_length,omitempty"`
	Quants        []Quant   `json:"quants"`
	Sidecars      []File    `json:"sidecars"` // mmproj / draft-model files
	URL           string    `json:"url"`
}

// Repo returns a repository's details and quantizations.
func (c *Client) Repo(ctx context.Context, id string) (*Detail, error) {
	if strings.Count(id, "/") != 1 || strings.ContainsAny(id, " ?#") {
		return nil, fmt.Errorf("invalid repository id %q", id)
	}
	var raw struct {
		ID           string          `json:"id"`
		Author       string          `json:"author"`
		Downloads    int64           `json:"downloads"`
		Likes        int64           `json:"likes"`
		LastModified time.Time       `json:"lastModified"`
		Gated        json.RawMessage `json:"gated"` // false or "auto"/"manual"
		PipelineTag  string          `json:"pipeline_tag"`
		CardData     struct {
			License   any `json:"license"`
			BaseModel any `json:"base_model"`
		} `json:"cardData"`
		GGUF struct {
			Total         int64  `json:"total"`
			Architecture  string `json:"architecture"`
			ContextLength int64  `json:"context_length"`
		} `json:"gguf"`
		Siblings []struct {
			RFilename string `json:"rfilename"`
			Size      int64  `json:"size"`
			LFS       *struct {
				SHA256 string `json:"sha256"`
				Size   int64  `json:"size"`
			} `json:"lfs"`
		} `json:"siblings"`
	}
	if err := c.get(ctx, "/api/models/"+id, url.Values{"blobs": {"true"}}, &raw); err != nil {
		return nil, err
	}
	d := &Detail{
		ID: raw.ID, Author: raw.Author, Downloads: raw.Downloads, Likes: raw.Likes,
		LastModified: raw.LastModified, PipelineTag: raw.PipelineTag,
		Gated:  len(raw.Gated) > 0 && string(raw.Gated) != "false",
		Params: raw.GGUF.Total, Architecture: raw.GGUF.Architecture, ContextLength: raw.GGUF.ContextLength,
		License: firstString(raw.CardData.License), BaseModel: firstString(raw.CardData.BaseModel),
		URL: BaseURL + "/" + raw.ID,
	}
	var files []File
	for _, s := range raw.Siblings {
		f := File{Path: s.RFilename, Size: s.Size}
		if s.LFS != nil {
			f.SHA256, f.Size = s.LFS.SHA256, s.LFS.Size
		}
		files = append(files, f)
	}
	d.Quants, d.Sidecars = GroupQuants(d.ID, files)
	return d, nil
}

func firstString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		if len(x) > 0 {
			s, _ := x[0].(string)
			return s
		}
	}
	return ""
}

// GroupQuants groups GGUF files into quantizations, following llama.cpp's rules
// (common/download.cpp): the tag is the last [-.]WORD of the file name, split files
// belong together, and mmproj/imatrix/draft files are not models.
func GroupQuants(repo string, files []File) ([]Quant, []File) {
	type key struct {
		prefix string
		count  int
	}
	groups := map[key]*Quant{}
	var order []key
	var sidecars []File
	for _, f := range files {
		si, ok := gguf.ParseSplit(f.Path)
		if !ok {
			continue
		}
		if !gguf.IsModelFile(f.Path) {
			if !strings.Contains(strings.ToLower(path.Base(f.Path)), "imatrix") {
				sidecars = append(sidecars, f)
			}
			continue
		}
		if si.Tag == "" {
			continue
		}
		k := key{si.Prefix, si.Count}
		q, ok := groups[k]
		if !ok {
			q = &Quant{Tag: si.Tag, Ref: repo + ":" + si.Tag, Bits: bitsOf(si.Tag), Dir: path.Dir(f.Path)}
			if q.Dir == "." {
				q.Dir = ""
			}
			groups[k] = q
			order = append(order, k)
		}
		q.Files = append(q.Files, f)
		q.Size += f.Size
	}

	// llama.cpp picks the first model file (in listing order) whose path matches TAG[.-];
	// with several groups sharing a tag only that first one is reachable by "repo:TAG".
	firstForTag := map[string]key{}
	for _, f := range files {
		si, ok := gguf.ParseSplit(f.Path)
		if !ok || !gguf.IsModelFile(f.Path) || si.Tag == "" || (si.Count > 1 && si.Index != 1) {
			continue
		}
		if _, seen := firstForTag[si.Tag]; !seen {
			firstForTag[si.Tag] = key{si.Prefix, si.Count}
		}
	}
	tagCount := map[string]int{}
	for _, k := range order {
		tagCount[groups[k].Tag]++
	}

	out := make([]Quant, 0, len(order))
	for _, k := range order {
		q := groups[k]
		q.Ambiguous = tagCount[q.Tag] > 1
		q.Selectable = firstForTag[q.Tag] == k
		sort.Slice(q.Files, func(i, j int) bool { return q.Files[i].Path < q.Files[j].Path })
		for _, s := range bestSidecars(sidecars, q) {
			q.Extras = append(q.Extras, s.Path)
			q.ExtraSize += s.Size
		}
		if q.Extras == nil {
			q.Extras = []string{}
		}
		out = append(out, *q)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Bits != out[j].Bits {
			return out[i].Bits < out[j].Bits
		}
		return out[i].Size < out[j].Size
	})
	if sidecars == nil {
		sidecars = []File{}
	}
	return out, sidecars
}

// bestSidecars approximates llama.cpp's find_best_sibling: per kind (mmproj, mtp-, ...), the
// file in the same or a parent directory with the closest quantization bits.
func bestSidecars(sidecars []File, q *Quant) []File {
	var out []File
	for _, kind := range []string{"mmproj", "mtp-"} {
		var best *File
		bestDiff := 1 << 30
		for i := range sidecars {
			f := &sidecars[i]
			if !strings.Contains(path.Base(f.Path), kind) {
				continue
			}
			if d := path.Dir(f.Path); d != "." && d != q.Dir && !strings.HasPrefix(q.Dir+"/", d+"/") {
				continue
			}
			si, _ := gguf.ParseSplit(f.Path)
			diff := abs(bitsOf(si.Tag) - q.Bits)
			if best == nil || diff < bestDiff {
				best, bestDiff = f, diff
			}
		}
		if best != nil {
			out = append(out, *best)
		}
	}
	return out
}

func bitsOf(tag string) int {
	i := strings.IndexAny(tag, "0123456789")
	if i < 0 {
		return 0
	}
	n := 0
	for ; i < len(tag) && tag[i] >= '0' && tag[i] <= '9'; i++ {
		n = n*10 + int(tag[i]-'0')
	}
	return n
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// FetchHeader reads the GGUF header (metadata + tensor table) of a repository file with HTTP
// range requests, without downloading the model. fileSize is the file's full size.
func (c *Client) FetchHeader(ctx context.Context, repo, path string, fileSize int64) (*gguf.Info, []gguf.Tensor, error) {
	u := BaseURL + "/" + repo + "/resolve/main/" + path
	var lastErr error
	for _, n := range []int64{4 << 20, 16 << 20, 64 << 20} {
		n = min(n, fileSize)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", n-1))
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		hc := &http.Client{Timeout: 2 * time.Minute}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, nil, err
		}
		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, nil, fmt.Errorf("fetch header of %s: HTTP %d", path, resp.StatusCode)
		}
		info, ts, err := gguf.ReadTensorsFrom(io.LimitReader(resp.Body, n), fileSize)
		resp.Body.Close()
		if err == nil {
			return info, ts, nil
		}
		lastErr = err
		if n >= fileSize {
			break
		}
	}
	return nil, nil, fmt.Errorf("parse header of %s: %w", path, lastErr)
}
