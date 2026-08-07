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
	Chapters   []store.Chapter
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
		"-v", "error", "-show_format", "-show_streams", "-show_chapters",
		"-of", "json", path).Output()
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

type probeSideData struct {
	Type string `json:"side_data_type"`
}

type probeJSON struct {
	Format struct {
		Duration   string `json:"duration"`
		FormatName string `json:"format_name"`
	} `json:"format"`
	Streams []struct {
		Index         int             `json:"index"`
		CodecType     string          `json:"codec_type"`
		CodecName     string          `json:"codec_name"`
		CodecTag      string          `json:"codec_tag_string"`
		Profile       string          `json:"profile"`
		Width         int             `json:"width"`
		Height        int             `json:"height"`
		Channels      int             `json:"channels"`
		ChannelLayout string          `json:"channel_layout"`
		ColorTransfer string          `json:"color_transfer"`
		Duration      string          `json:"duration"`
		SideData      []probeSideData `json:"side_data_list"`
		Tags          struct {
			Language string `json:"language"`
			Title    string `json:"title"`
		} `json:"tags"`
		Disposition struct {
			Default     int `json:"default"`
			Forced      int `json:"forced"`
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
	} `json:"streams"`
	Chapters []struct {
		StartTime string `json:"start_time"`
		Tags      struct {
			Title string `json:"title"`
		} `json:"tags"`
	} `json:"chapters"`
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
		if rawStream.CodecType == "video" && rawStream.Disposition.AttachedPic != 0 {
			continue
		}
		stream := store.Stream{
			Index: rawStream.Index, Kind: rawStream.CodecType, Codec: rawStream.CodecName,
			Profile: rawStream.Profile, Language: rawStream.Tags.Language, Title: rawStream.Tags.Title,
			Width: rawStream.Width, Height: rawStream.Height, Channels: rawStream.Channels,
			ChannelLayout: rawStream.ChannelLayout,
			IsDefault:     rawStream.Disposition.Default != 0,
			IsForced:      rawStream.Disposition.Forced != 0,
		}
		if rawStream.CodecType == "video" {
			stream.DynamicRange = dynamicRange(rawStream.CodecTag, rawStream.ColorTransfer, rawStream.SideData)
		}
		result.Streams = append(result.Streams, stream)
		if result.DurationMS == 0 {
			result.DurationMS = durationMS(rawStream.Duration)
		}
	}
	result.Chapters = chapters(raw)
	return result, nil
}

// chapters keeps ffprobe's file order and numbers marks from there, so a client
// can present "chapter 3" without re-deriving it. A file whose only chapter
// spans the whole runtime carries no navigable structure, so it is dropped
// rather than handed to every client to filter out.
func chapters(raw probeJSON) []store.Chapter {
	if len(raw.Chapters) < 2 {
		return nil
	}
	list := make([]store.Chapter, 0, len(raw.Chapters))
	for index, rawChapter := range raw.Chapters {
		list = append(list, store.Chapter{
			Index:   index,
			StartMS: durationMS(rawChapter.StartTime),
			Title:   rawChapter.Tags.Title,
		})
	}
	return list
}

func dynamicRange(codecTag, colorTransfer string, sideData []probeSideData) string {
	codecTag = strings.ToLower(codecTag)
	if codecTag == "dvhe" || codecTag == "dvh1" {
		return "dolby_vision"
	}
	for _, data := range sideData {
		if strings.Contains(strings.ToLower(data.Type), "dovi") ||
			strings.Contains(strings.ToLower(data.Type), "dolby vision") {
			return "dolby_vision"
		}
	}
	switch strings.ToLower(colorTransfer) {
	case "smpte2084", "arib-std-b67":
		return "hdr"
	default:
		return "sdr"
	}
}

func durationMS(value string) int64 {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || seconds < 0 {
		return 0
	}
	return int64(seconds * 1000)
}
