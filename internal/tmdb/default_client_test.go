package tmdb

import (
 "testing"
 "time"
)

func TestNewUsesProductionEndpointsAndTimeout(t *testing.T) {
 client := New("key", "en-US")
 if client.apiKey != "key" || client.language != "en-US" || client.baseURL != DefaultBaseURL || client.imageURL != DefaultImageURL || client.http.Timeout != 20*time.Second {
  t.Fatalf("default TMDB client = %+v", client)
 }
}
