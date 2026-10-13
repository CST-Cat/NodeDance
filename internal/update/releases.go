package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	API  string
}

func NewGitHub() *GitHub {
	return &GitHub{API: "https://api.github.com", HTTP: &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || req.URL.Scheme != "https" {
			return errors.New("unsafe release redirect")
		}
		switch req.URL.Hostname() {
		case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
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

func (g *GitHub) Resolve(ctx context.Context, component, tag string) (Release, error) {
	if err := Validate(Request{Component: component, Version: tag}); err != nil {
		return Release{}, err
	}
	var selected githubRelease
	if tag != "" {
		raw, err := g.fetch(ctx, g.API+"/repos/"+Repository+"/releases/tags/"+url.PathEscape(tag), 4<<20)
		if err != nil {
			return Release{}, err
		}
		if err = json.Unmarshal(raw, &selected); err != nil {
			return Release{}, err
		}
	} else {
		for page := 1; page <= 20; page++ {
			raw, err := g.fetch(ctx, fmt.Sprintf("%s/repos/%s/releases?per_page=100&page=%d", g.API, Repository, page), 8<<20)
			if err != nil {
				return Release{}, err
			}
			var entries []githubRelease
			if err = json.Unmarshal(raw, &entries); err != nil {
				return Release{}, err
			}
			for _, entry := range entries {
				if !entry.Draft && !entry.Prerelease && ValidTag(component, entry.Tag) && (selected.Tag == "" || Compare(entry.Tag, selected.Tag) > 0) {
					selected = entry
				}
			}
			if len(entries) < 100 {
				break
			}
			if page == 20 {
				return Release{}, errors.New("release listing exceeds supported limit")
			}
		}
	}
	if selected.Draft || selected.Prerelease || !ValidTag(component, selected.Tag) {
		return Release{}, errors.New("no published stable component release")
	}
	release := Release{URL: selected.URL, Notes: selected.Body}
	if len(release.Notes) > 32768 {
		release.Notes = release.Notes[:32768]
	}
	var manifestURL string
	binary := "nodedance-linux-" + runtime.GOARCH
	if component == "agent" {
		binary = "nodedance-agent-linux-" + runtime.GOARCH
	}
	for _, asset := range selected.Assets {
		prefix := "https://github.com/" + Repository + "/releases/download/" + selected.Tag + "/"
		if asset.URL != prefix+asset.Name {
			return Release{}, errors.New("unexpected release asset URL")
		}
		switch asset.Name {
		case "nodedance-update.json":
			manifestURL = asset.URL
		case "SHA256SUMS":
			release.ChecksumURL = asset.URL
		case binary:
			release.BinaryURL = asset.URL
		}
	}
	if manifestURL == "" || release.BinaryURL == "" || release.ChecksumURL == "" {
		return Release{}, errors.New("release does not include an update manifest and verified native artifacts")
	}
	raw, err := g.fetch(ctx, manifestURL, 64<<10)
	if err != nil {
		return Release{}, err
	}
	if err = Decode(raw, &release.Manifest); err != nil {
		return Release{}, err
	}
	if release.Format != 1 || release.Component != component || release.Version != selected.Tag {
		return Release{}, errors.New("release manifest identity mismatch")
	}
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
