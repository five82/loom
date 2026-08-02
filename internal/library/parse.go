package library

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	yearSuffixPattern = regexp.MustCompile(`^(.*?)\s+\(([0-9]{4})\)$`)
	episodePattern    = regexp.MustCompile(`(?i)\bS([0-9]{1,4})E([0-9]{1,4})(?:-(?:E)?([0-9]{1,4}))?\b`)
)

var videoExtensions = map[string]bool{
	".avi":  true,
	".m4v":  true,
	".mkv":  true,
	".mov":  true,
	".mp4":  true,
	".mpeg": true,
	".mpg":  true,
	".ts":   true,
	".webm": true,
}

func isVideo(path string) bool {
	return videoExtensions[strings.ToLower(filepath.Ext(path))]
}

func parseNamedYear(name string) (string, int) {
	match := yearSuffixPattern.FindStringSubmatch(strings.TrimSpace(name))
	if match == nil {
		return strings.TrimSpace(name), 0
	}
	year, _ := strconv.Atoi(match[2])
	return strings.TrimSpace(match[1]), year
}

type episodeNumbers struct {
	Season int
	Start  int
	End    int
}

func parseEpisodeFilename(name string) (episodeNumbers, bool) {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	match := episodePattern.FindStringSubmatch(base)
	if match == nil {
		return episodeNumbers{}, false
	}
	season, err := strconv.Atoi(match[1])
	if err != nil {
		return episodeNumbers{}, false
	}
	start, err := strconv.Atoi(match[2])
	if err != nil || start < 1 {
		return episodeNumbers{}, false
	}
	end := start
	if match[3] != "" {
		end, err = strconv.Atoi(match[3])
		if err != nil || end < start {
			return episodeNumbers{}, false
		}
	}
	return episodeNumbers{Season: season, Start: start, End: end}, true
}

func episodeTitle(numbers episodeNumbers) string {
	if numbers.End > numbers.Start {
		return "Episodes " + strconv.Itoa(numbers.Start) + "-" + strconv.Itoa(numbers.End)
	}
	return "Episode " + strconv.Itoa(numbers.Start)
}
