package library

import "testing"

func TestProbeSkipsUnusableStreamsAndUsesStreamDuration(t *testing.T) {
	data := []byte(`{"format":{"format_name":"matroska"},"streams":[{"index":0,"codec_type":"attachment"},{"index":1,"codec_type":"video","disposition":{"attached_pic":1}},{"index":2,"codec_type":"audio","duration":"2.5","codec_name":"aac"}]}`)
	result, err := parseProbeOutput(data)
	if err != nil || result.DurationMS != 2500 || len(result.Streams) != 1 || result.Streams[0].Kind != "audio" {
		t.Fatalf("probe = %+v, %v", result, err)
	}
	if durationMS("bad") != 0 || durationMS("-1") != 0 {
		t.Fatal("invalid durations must not be persisted")
	}
}
