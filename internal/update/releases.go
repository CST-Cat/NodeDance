package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

type Resolver interface {
	Resolve(context.Context, string, string) (Release, error)
	Download(context.Context, Release, string) error
}
type GitHub struct {
	HTTP *http.Client
}

func NewGitHub() *GitHub {
	return &GitHub{HTTP: &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || req.URL.Scheme != "https" {
			return errors.New("unsafe release redirect")
		}
		switch req.URL.Hostname() {
		case "github.com", "raw.githubusercontent.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
			return nil
		}
		return errors.New("unexpected release download host")
	}}}
}
func (g *GitHub) fetch(ctx context.Context, address string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "NodeDance")
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := g.HTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("release service: HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err == nil && int64(len(raw)) > limit {
		return nil, errors.New("release response exceeds limit")
	}
	return raw, err
}

type githubRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	URL        string `json:"html_url"`
	Body       string `json:"body"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

const (
	// Version file published by the release workflows on main. Resolving the
	// latest tag through it keeps update checks on plain HTTPS downloads and
	// away from the GitHub REST API's per-IP rate limits.
	latestVersionURL = "https://raw.githubusercontent.com/" + Repository + "/main/%s-latest.txt"
	releaseBaseURL   = "https://github.com/" + Repository + "/releases/download/"
)

func (g *GitHub) Resolve(ctx context.Context, component, tag string) (Release, error) {
	if err := Validate(Request{Component: component, Version: tag}); err != nil {
		return Release{}, err
	}
	if tag == "" {
		raw, err := g.fetch(ctx, fmt.Sprintf(latestVersionURL, component), 4096)
		if err != nil {
			return Release{}, err
		}
		tag = strings.TrimSpace(string(raw))
		if !ValidTag(component, tag) {
			return Release{}, errors.New("the published version file does not contain a valid stable " + component + " tag")
		}
	}
	// A pinned or resolved tag needs no release metadata: the update
	// transaction verifies integrity through the tag's SHA256SUMS asset.
	selected := githubRelease{Tag: tag, URL: "https://github.com/" + Repository + "/releases/tag/" + tag}
	release := Release{Component: component, Version: selected.Tag, URL: selected.URL}
	binary := "nodedance-linux-" + runtime.GOARCH
	if component == "agent" {
		binary = "nodedance-agent-linux-" + runtime.GOARCH
	}
	prefix := releaseBaseURL + selected.Tag + "/"
	release.ChecksumURL = prefix + "SHA256SUMS"
	release.BinaryURL = prefix + binary
	return release, nil
}
func (g *GitHub) Download(ctx context.Context, release Release, path string) error {
	raw, err := g.fetch(ctx, release.ChecksumURL, 1<<20)
	if err != nil {
		return err
	}
	asset := release.BinaryURL[strings.LastIndex(release.BinaryURL, "/")+1:]
	digest := ""
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			if digest != "" {
				return errors.New("duplicate release checksum")
			}
			digest = fields[0]
		}
	}
	expected, err := hex.DecodeString(digest)
	if err != nil || len(expected) != sha256.Size {
		return errors.New("missing valid release checksum")
	}
	request, err := http.NewRequestWithContext(ctx, "GET", release.BinaryURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "NodeDance")
	response, err := g.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("binary download: HTTP %d", response.StatusCode)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	defer file.Close()
	hasher := sha256.New()
	count, err := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(response.Body, (256<<20)+1))
	if err != nil {
		return err
	}
	if count > 256<<20 {
		return errors.New("release binary exceeds limit")
	}
	if hex.EncodeToString(hasher.Sum(nil)) != digest {
		return errors.New("release SHA256 verification failed")
	}
	return file.Sync()
}
