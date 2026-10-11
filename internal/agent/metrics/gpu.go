package metrics

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < b.limit {
		b.Buffer.Write(p[:min(n, b.limit-b.Len())])
	}
	return n, nil
}
func collectGPUs(parent context.Context) ([]protocol.GPUDevice, error) {
	devices := []protocol.GPUDevice{}
	if path, err := exec.LookPath("nvidia-smi"); err == nil {
		ctx, cancel := context.WithTimeout(parent, 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, "--query-gpu=uuid,name,utilization.gpu,memory.total,memory.used,temperature.gpu", "--format=csv,noheader,nounits")
		output := limitedBuffer{limit: 65536}
		cmd.Stdout = &output
		if cmd.Run() == nil {
			devices = append(devices, parseNvidia(output.Bytes())...)
		}
	}
	root := os.Getenv("HOST_SYS")
	if root == "" {
		root = "/sys"
	}
	cards, _ := filepath.Glob(filepath.Join(root, "class/drm/card[0-9]*"))
	for _, card := range cards {
		if strings.Contains(filepath.Base(card), "-") {
			continue
		}
		vendor, _ := os.ReadFile(filepath.Join(card, "device/vendor"))
		if strings.TrimSpace(string(vendor)) != "0x1002" {
			continue
		}
		g := protocol.GPUDevice{ID: filepath.Base(card), Name: "AMD " + filepath.Base(card)}
		if b, e := os.ReadFile(filepath.Join(card, "device/product_name")); e == nil {
			g.Name = strings.TrimSpace(string(b))
		}
		g.Utilization = readFloat(filepath.Join(card, "device/gpu_busy_percent"), 1)
		g.MemoryTotalBytes = readUint(filepath.Join(card, "device/mem_info_vram_total"))
		g.MemoryUsedBytes = readUint(filepath.Join(card, "device/mem_info_vram_used"))
		sensors, _ := filepath.Glob(filepath.Join(card, "device/hwmon/hwmon*/temp1_input"))
		if len(sensors) > 0 {
			g.TemperatureCelsius = readFloat(sensors[0], 1000)
		}
		devices = append(devices, sanitizeGPU(g))
	}
	if len(devices) == 0 {
		return nil, errors.New("no GPU driver available")
	}
	if len(devices) > 32 {
		devices = devices[:32]
	}
	return devices, nil
}
func parseNvidia(data []byte) []protocol.GPUDevice {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.TrimLeadingSpace = true
	out := []protocol.GPUDevice{}
	for len(out) < 32 {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if len(row) != 6 {
			continue
		}
		g := protocol.GPUDevice{ID: strings.TrimSpace(row[0]), Name: strings.TrimSpace(row[1])}
		g.Utilization = parseFloat(row[2], 1)
		g.TemperatureCelsius = parseFloat(row[5], 1)
		if n, e := strconv.ParseUint(strings.TrimSpace(row[3]), 10, 64); e == nil && n <= ^uint64(0)/(1024*1024) {
			n *= 1024 * 1024
			g.MemoryTotalBytes = &n
		}
		if n, e := strconv.ParseUint(strings.TrimSpace(row[4]), 10, 64); e == nil && n <= ^uint64(0)/(1024*1024) {
			n *= 1024 * 1024
			g.MemoryUsedBytes = &n
		}
		out = append(out, sanitizeGPU(g))
	}
	return out
}
func parseFloat(s string, scale float64) *float64 {
	n, e := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return nil
	}
	n /= scale
	return &n
}
func readFloat(path string, scale float64) *float64 {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil
	}
	return parseFloat(string(b), scale)
}
func readUint(path string) *uint64 {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil
	}
	n, e := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if e != nil {
		return nil
	}
	return &n
}

func sanitizeGPU(g protocol.GPUDevice) protocol.GPUDevice {
	if len(g.ID) > 128 {
		g.ID = clipUTF8(g.ID, 128)
	}
	if len(g.Name) > 256 {
		g.Name = clipUTF8(g.Name, 256)
	}
	if g.Utilization != nil && *g.Utilization > 100 {
		g.Utilization = nil
	}
	if g.TemperatureCelsius != nil && *g.TemperatureCelsius > 250 {
		g.TemperatureCelsius = nil
	}
	if g.MemoryUsedBytes != nil && g.MemoryTotalBytes != nil && *g.MemoryUsedBytes > *g.MemoryTotalBytes {
		g.MemoryUsedBytes = nil
	}
	return g
}

func clipUTF8(s string, limit int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) > limit {
		s = s[:limit]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}
