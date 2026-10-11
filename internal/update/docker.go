package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Docker struct {
	HTTP    *http.Client
	version string
}

func NewDocker() *Docker {
	return &Docker{HTTP: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}}}}
}
func (d *Docker) call(ctx context.Context, method, path string, body any, result any) error {
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, input)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified && method == "POST" && (strings.HasSuffix(path, "/start") || strings.Contains(path, "/stop?")) {
		return nil
	}
	if response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Docker HTTP %d: %s", response.StatusCode, raw)
	}
	if result != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(result)
	}
	_, err = io.Copy(io.Discard, response.Body)
	return err
}
func (d *Docker) initialize(ctx context.Context) error {
	var v struct {
		Version string `json:"ApiVersion"`
	}
	if err := d.call(ctx, "GET", "/version", nil, &v); err != nil {
		return err
	}
	if !strings.HasPrefix(v.Version, "1.") {
		return errors.New("invalid Docker API version")
	}
	for _, r := range strings.TrimPrefix(v.Version, "1.") {
		if r < '0' || r > '9' {
			return errors.New("invalid Docker API version")
		}
	}
	d.version = "/v" + v.Version
	return nil
}
func (d *Docker) inspect(ctx context.Context, name string) (map[string]any, error) {
	var result map[string]any
	err := d.call(ctx, "GET", d.version+"/containers/"+url.PathEscape(name)+"/json", nil, &result)
	return result, err
}
func (d *Docker) pull(ctx context.Context, reference string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, "POST", "http://docker"+d.version+"/images/create?fromImage="+url.QueryEscape(reference), nil)
	if err != nil {
		return "", err
	}
	response, err := d.HTTP.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return "", fmt.Errorf("Docker image pull HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(response.Body)
	for {
		var item struct {
			Error string `json:"error"`
		}
		err = decoder.Decode(&item)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if item.Error != "" {
			return "", errors.New(item.Error)
		}
	}
	var image struct {
		ID string `json:"Id"`
	}
	if err = d.call(ctx, "GET", d.version+"/images/"+url.PathEscape(reference)+"/json", nil, &image); err != nil {
		return "", err
	}
	if !strings.HasPrefix(image.ID, "sha256:") || len(image.ID) != 71 {
		return "", errors.New("invalid pulled Docker image digest")
	}
	return image.ID, nil
}
func (d *Docker) stop(ctx context.Context, id string) error {
	return d.call(ctx, "POST", d.version+"/containers/"+id+"/stop?t=30", nil, nil)
}
func (d *Docker) start(ctx context.Context, id string) error {
	return d.call(ctx, "POST", d.version+"/containers/"+id+"/start", nil, nil)
}
func (d *Docker) rename(ctx context.Context, id, name string) error {
	value, err := d.inspect(ctx, id)
	if err != nil {
		return err
	}
	if value["Name"] == "/"+name {
		return nil
	}
	return d.call(ctx, "POST", d.version+"/containers/"+id+"/rename?name="+url.QueryEscape(name), nil, nil)
}
func (d *Docker) remove(ctx context.Context, id string) error {
	return d.call(ctx, "DELETE", d.version+"/containers/"+id+"?force=true&v=false", nil, nil)
}
func networks(value map[string]any) map[string]any {
	host, _ := value["HostConfig"].(map[string]any)
	mode, _ := host["NetworkMode"].(string)
	if mode == "host" || mode == "none" || strings.HasPrefix(mode, "container:") {
		return nil
	}
	settings, _ := value["NetworkSettings"].(map[string]any)
	result, _ := settings["Networks"].(map[string]any)
	return result
}
func (d *Docker) disconnect(ctx context.Context, id string, value map[string]any) error {
	for name := range networks(value) {
		if err := d.call(ctx, "POST", d.version+"/networks/"+url.PathEscape(name)+"/disconnect", map[string]any{"Container": id, "Force": true}, nil); err != nil {
			return err
		}
	}
	return nil
}
func endpoint(value any) map[string]any {
	source, _ := value.(map[string]any)
	result := map[string]any{}
	for _, key := range []string{"IPAMConfig", "Aliases", "DriverOpts"} {
		if source[key] != nil {
			result[key] = source[key]
		}
	}
	return result
}
func (d *Docker) connect(ctx context.Context, id string, value map[string]any) error {
	current, err := d.inspect(ctx, id)
	if err != nil {
		return err
	}
	for name, ep := range networks(value) {
		if _, ok := networks(current)[name]; ok {
			continue
		}
		if err := d.call(ctx, "POST", d.version+"/networks/"+url.PathEscape(name)+"/connect", map[string]any{"Container": id, "EndpointConfig": endpoint(ep)}, nil); err != nil {
			return err
		}
	}
	return nil
}
func (d *Docker) create(ctx context.Context, name, image, job string, old map[string]any) (string, error) {
	config, _ := old["Config"].(map[string]any)
	if config == nil {
		return "", errors.New("Docker container config is missing")
	}
	raw, _ := json.Marshal(config)
	var body map[string]any
	json.Unmarshal(raw, &body)
	body["Image"] = image
	labels, _ := body["Labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	labels["io.nodedance.update.job"] = job
	body["Labels"] = labels
	body["HostConfig"] = old["HostConfig"]
	endpoints := map[string]any{}
	for name, value := range networks(old) {
		endpoints[name] = endpoint(value)
	}
	body["NetworkingConfig"] = map[string]any{"EndpointsConfig": endpoints}
	var result struct {
		ID string `json:"Id"`
	}
	err := d.call(ctx, "POST", d.version+"/containers/create?name="+url.QueryEscape(name), body, &result)
	return result.ID, err
}
func healthy(value map[string]any) bool {
	state, _ := value["State"].(map[string]any)
	if state["Running"] != true {
		return false
	}
	health, _ := state["Health"].(map[string]any)
	return health == nil || health["Status"] == "healthy"
}
func pause(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
