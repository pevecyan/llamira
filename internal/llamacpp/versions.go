package llamacpp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GitHubAPI is the GitHub API base URL (a variable for tests).
var GitHubAPI = "https://api.github.com"

// Versions are llama.cpp's semantic version tags (v0.6.0, …). Each version release has a
// nightly-tag.txt asset naming the build (bNNNNN) it was cut from, so the build pipeline
// (prebuilt download, source build, switch) keeps working on build tags.

var reVersion = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// maxVersions is how many of the newest versions are looked up (one API call each).
const maxVersions = 8

var (
	verMu    sync.Mutex
	verCache []Release
	verTime  time.Time
)

var ghClient = &http.Client{Timeout: 20 * time.Second}

func ghGet(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := ghClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GitHub: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if s, ok := out.(*string); ok {
		b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		*s = string(b)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// versionLess orders v0.10.0 after v0.9.0.
func versionLess(a, b string) bool {
	ma, mb := reVersion.FindStringSubmatch(a), reVersion.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(ma[i])
		y, _ := strconv.Atoi(mb[i])
		if x != y {
			return x < y
		}
	}
	return false
}

// Versions lists the newest version releases, newest first, each with the build tag it was cut
// from (cached for 6 hours). Versions without a release or without a build tag are left out.
func Versions(ctx context.Context) ([]Release, error) {
	verMu.Lock()
	defer verMu.Unlock()
	if verCache != nil && time.Since(verTime) < 6*time.Hour {
		return verCache, nil
	}
	var tags []struct {
		Name string `json:"name"`
	}
	if err := ghGet(ctx, GitHubAPI+"/repos/ggml-org/llama.cpp/tags?per_page=100", &tags); err != nil {
		return nil, err
	}
	var names []string
	for _, t := range tags {
		if reVersion.MatchString(t.Name) {
			names = append(names, t.Name)
		}
	}
	sort.Slice(names, func(i, j int) bool { return versionLess(names[j], names[i]) })
	if len(names) > maxVersions {
		names = names[:maxVersions]
	}
	out := []Release{}
	for _, v := range names {
		var rel struct {
			PublishedAt time.Time `json:"published_at"`
			HTMLURL     string    `json:"html_url"`
			Body        string    `json:"body"`
			Draft       bool      `json:"draft"`
			Assets      []struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
			} `json:"assets"`
		}
		if err := ghGet(ctx, GitHubAPI+"/repos/ggml-org/llama.cpp/releases/tags/"+v, &rel); err != nil || rel.Draft {
			continue // a tag without a release
		}
		build := ""
		for _, a := range rel.Assets {
			if a.Name == "nightly-tag.txt" {
				var txt string
				if ghGet(ctx, a.URL, &txt) == nil {
					build = strings.TrimSpace(txt)
				}
			}
		}
		if !strings.HasPrefix(build, "b") || !ValidRef(build) {
			continue
		}
		out = append(out, Release{Tag: build, Version: v, Published: rel.PublishedAt, URL: rel.HTMLURL, Notes: releaseTitle(rel.Body)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no version releases found")
	}
	verCache, verTime = out, time.Now()
	return out, nil
}

// VersionOf returns the newest version whose build is at or below the given build number
// ("v0.4.1" for b11036 when v0.4.1 is b10980), and how many builds later the build is.
func VersionOf(versions []Release, build int) (string, int) {
	best, bestN := "", 0
	for _, v := range versions {
		n := BuildNumber(v.Tag)
		if n > 0 && n <= build && n > bestN {
			best, bestN = v.Version, n
		}
	}
	if best == "" {
		return "", 0
	}
	return best, build - bestN
}
