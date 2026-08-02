package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/five82/loom/internal/store"
)

// ProbeResult is the subset of ffprobe output needed by Loom and its client.
type ProbeResult struct {
	DurationMS int64
	Container  string
	Streams    []store.Stream
}

// Prober inspects a media file without modifying it.
type Prober interface {
	Probe(context.Context, string) (ProbeResult, error)
}

type FFProber struct {
	Command string
	Timeout time.Duration
}

func NewFFProber(command string) *FFProber {
	return &FFProber{Command: command, Timeout: 2 * time.Minute}
}

func (p *FFProber) Probe(ctx context.Context, path string) (ProbeResult, error) {
	probeCtx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, p.Command,
		"-v", "error", "-show_format", "-show_streams", "-of", "json", path).Output()
	if err != nil {
		if probeCtx.Err() != nil {
			return ProbeResult{}, fmt.Errorf("ffprobe %q: %w", path, probeCtx.Err())
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			message := strings.TrimSpace(string(exitErr.Stderr))
			if message != "" {
				return ProbeResult{}, fmt.Errorf("ffprobe %q: %s", path, message)
			}
		}
		return ProbeResult{}, fmt.Errorf("ffprobe %q: %w", path, err)
	}
	return parseProbeOutput(output)
}

type probeJSON struct {
	Format struct {
		Duration   string `json:"duration"`
		FormatName string `json:"format_name"`
	} `json:"format"`
	Streams []struct {
		Index     int    `json:"index"`
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		Channels  int    `json:"channels"`
		Duration  string `json:"duration"`
		Tags      struct {
			Language string `json:"language"`
			Title    string `json:"title"`
		} `json:"tags"`
		Disposition struct {
			Default int `json:"default"`
			Forced  int `json:"forced"`
		} `json:"disposition"`
	} `json:"streams"`
}

func parseProbeOutput(data []byte) (ProbeResult, error) {
	var raw probeJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return ProbeResult{}, fmt.Errorf("decode ffprobe output: %w", err)
	}
	result := ProbeResult{Container: raw.Format.FormatName}
	result.DurationMS = durationMS(raw.Format.Duration)
	for _, rawStream := range raw.Streams {
		if rawStream.CodecType != "video" && rawStream.CodecType != "audio" && rawStream.CodecType != "subtitle" {
			continue
		}
		result.Streams = append(result.Streams, store.Stream{
			Index: rawStream.Index, Kind: rawStream.CodecType, Codec: rawStream.CodecName,
			Language: rawStream.Tags.Language, Title: rawStream.Tags.Title,
			Width: rawStream.Width, Height: rawStream.Height, Channels: rawStream.Channels,
			IsDefault: rawStream.Disposition.Default != 0,
			IsForced:  rawStream.Disposition.Forced != 0,
		})
		if result.DurationMS == 0 {
			result.DurationMS = durationMS(rawStream.Duration)
		}
	}
	return result, nil
}

func durationMS(value string) int64 {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || seconds < 0 {
		return 0
	}
	return int64(seconds * 1000)
}
