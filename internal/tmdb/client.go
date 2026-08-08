package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL  = "https://api.themoviedb.org/3"
	DefaultImageURL = "https://image.tmdb.org/t/p"
)

type HTTPError struct {
	StatusCode int
	Status     string
	Detail     string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("TMDB returned %s: %s", e.Status, e.Detail)
}

type Client struct {
	apiKey   string
	language string
	baseURL  string
	imageURL string
	http     *http.Client
}

func New(apiKey, language string) *Client {
	return NewWithURLs(apiKey, language, DefaultBaseURL, DefaultImageURL, &http.Client{Timeout: 20 * time.Second})
}

func NewWithURLs(apiKey, language, baseURL, imageURL string, client *http.Client) *Client {
	return &Client{
		apiKey: apiKey, language: language, baseURL: strings.TrimRight(baseURL, "/"),
		imageURL: strings.TrimRight(imageURL, "/"), http: client,
	}
}

type SearchResult struct {
	ID            int64   `json:"id"`
	MediaType     string  `json:"media_type"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title,omitempty"`
	Overview      string  `json:"overview,omitempty"`
	ReleaseDate   string  `json:"release_date,omitempty"`
	Year          int     `json:"year,omitempty"`
	PosterPath    string  `json:"poster_path,omitempty"`
	VoteAverage   float64 `json:"vote_average,omitempty"`
	VoteCount     int     `json:"vote_count,omitempty"`
}

func (c *Client) Search(ctx context.Context, mediaType, query string, year int) ([]SearchResult, error) {
	if mediaType != "movie" && mediaType != "tv" {
		return nil, fmt.Errorf("unsupported TMDB media type %q", mediaType)
	}
	values := url.Values{"query": {query}}
	if year > 0 {
		key := "year"
		if mediaType == "tv" {
			key = "first_air_date_year"
		}
		values.Set(key, strconv.Itoa(year))
	}
	var response struct {
		Results []struct {
			ID            int64   `json:"id"`
			Title         string  `json:"title"`
			Name          string  `json:"name"`
			OriginalTitle string  `json:"original_title"`
			OriginalName  string  `json:"original_name"`
			Overview      string  `json:"overview"`
			ReleaseDate   string  `json:"release_date"`
			FirstAirDate  string  `json:"first_air_date"`
			PosterPath    string  `json:"poster_path"`
			VoteAverage   float64 `json:"vote_average"`
			VoteCount     int     `json:"vote_count"`
		} `json:"results"`
	}
	if err := c.get(ctx, "/search/"+mediaType, values, &response); err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0, len(response.Results))
	for _, raw := range response.Results {
		title, original, date := raw.Title, raw.OriginalTitle, raw.ReleaseDate
		if mediaType == "tv" {
			title, original, date = raw.Name, raw.OriginalName, raw.FirstAirDate
		}
		results = append(results, SearchResult{
			ID: raw.ID, MediaType: mediaType, Title: title, OriginalTitle: original,
			Overview: raw.Overview, ReleaseDate: date, Year: dateYear(date), PosterPath: raw.PosterPath,
			VoteAverage: raw.VoteAverage, VoteCount: raw.VoteCount,
		})
	}
	return results, nil
}

type Genre struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// CastCredit is one billed acting role. Character is routinely empty on older
// or lightly edited entries, so a client cannot rely on it to label a name.
type CastCredit struct {
	ID        int64
	Name      string
	Character string
}

// Director is one directing credit. TV directors are per episode rather than
// per show, so this is only meaningful for movies.
type Director struct {
	ID   int64
	Name string
}

type Details struct {
	ID           int64
	Title        string
	Tagline      string
	Overview     string
	ReleaseDate  string
	Year         int
	PosterPath   string
	BackdropPath string
	Genres       []Genre
	VoteAverage  float64
	// ContentRating is the certification for the client's own region, such as
	// "R" or "TV-14". TMDB publishes one per country, so an absent region
	// simply leaves this empty rather than borrowing another country's board.
	ContentRating string
	// Status and TotalSeasons describe a show's whole run, including seasons
	// this library does not hold. Both stay zero for movies, whose "Released"
	// status tells a detail screen nothing.
	Status       string
	TotalSeasons int
	// Cast is every billed role in TMDB's billing order. Callers take the top
	// of it rather than storing a whole crawl of bit parts.
	Cast      []CastCredit
	Directors []Director
}

func (c *Client) Details(ctx context.Context, mediaType string, id int64) (Details, error) {
	if mediaType != "movie" && mediaType != "tv" {
		return Details{}, fmt.Errorf("unsupported TMDB media type %q", mediaType)
	}
	// Certifications and credits live on sibling endpoints that
	// append_to_response folds into this response, so a detail screen still
	// costs one request.
	appended := "release_dates,credits"
	if mediaType == "tv" {
		appended = "content_ratings,credits"
	}
	var raw struct {
		ID              int64   `json:"id"`
		Title           string  `json:"title"`
		Name            string  `json:"name"`
		Tagline         string  `json:"tagline"`
		Overview        string  `json:"overview"`
		ReleaseDate     string  `json:"release_date"`
		FirstAirDate    string  `json:"first_air_date"`
		PosterPath      string  `json:"poster_path"`
		BackdropPath    string  `json:"backdrop_path"`
		Genres          []Genre `json:"genres"`
		VoteAverage     float64 `json:"vote_average"`
		Status          string  `json:"status"`
		NumberOfSeasons int     `json:"number_of_seasons"`
		ReleaseDates    struct {
			Results []struct {
				Country      string `json:"iso_3166_1"`
				ReleaseDates []struct {
					Certification string `json:"certification"`
				} `json:"release_dates"`
			} `json:"results"`
		} `json:"release_dates"`
		ContentRatings struct {
			Results []struct {
				Country string `json:"iso_3166_1"`
				Rating  string `json:"rating"`
			} `json:"results"`
		} `json:"content_ratings"`
		Credits struct {
			Cast []struct {
				ID        int64  `json:"id"`
				Name      string `json:"name"`
				Character string `json:"character"`
				Order     int    `json:"order"`
			} `json:"cast"`
			Crew []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
				Job  string `json:"job"`
			} `json:"crew"`
		} `json:"credits"`
	}
	values := url.Values{"append_to_response": {appended}}
	if err := c.get(ctx, "/"+mediaType+"/"+strconv.FormatInt(id, 10), values, &raw); err != nil {
		return Details{}, err
	}
	title, date := raw.Title, raw.ReleaseDate
	if mediaType == "tv" {
		title, date = raw.Name, raw.FirstAirDate
	}
	region := c.certificationRegion()
	rating := ""
	if mediaType == "tv" {
		for _, result := range raw.ContentRatings.Results {
			if strings.EqualFold(result.Country, region) {
				rating = result.Rating
				break
			}
		}
	} else {
		for _, result := range raw.ReleaseDates.Results {
			if !strings.EqualFold(result.Country, region) {
				continue
			}
			// A country lists one entry per release window (theatrical,
			// digital, physical) and only some of them carry a certification.
			for _, release := range result.ReleaseDates {
				if release.Certification != "" {
					rating = release.Certification
					break
				}
			}
			break
		}
	}
	details := Details{
		ID: raw.ID, Title: title, Tagline: raw.Tagline, Overview: raw.Overview, ReleaseDate: date,
		Year: dateYear(date), PosterPath: raw.PosterPath, BackdropPath: raw.BackdropPath,
		Genres: raw.Genres, VoteAverage: raw.VoteAverage, ContentRating: rating,
	}
	// TMDB usually returns cast in billing order but does not promise it, and
	// the order field is what the billing actually is.
	sort.SliceStable(raw.Credits.Cast, func(a, b int) bool {
		return raw.Credits.Cast[a].Order < raw.Credits.Cast[b].Order
	})
	for _, member := range raw.Credits.Cast {
		if member.Name == "" {
			continue
		}
		details.Cast = append(details.Cast, CastCredit{
			ID: member.ID, Name: member.Name, Character: member.Character,
		})
	}
	for _, member := range raw.Credits.Crew {
		if member.Job != "Director" || member.Name == "" {
			continue
		}
		details.Directors = append(details.Directors, Director{ID: member.ID, Name: member.Name})
	}
	if mediaType == "tv" {
		details.Status, details.TotalSeasons = raw.Status, raw.NumberOfSeasons
	}
	return details, nil
}

// certificationRegion reads the country out of the configured language tag, so
// "en-US" picks the MPA and TV Parental Guidelines ratings.
func (c *Client) certificationRegion() string {
	if _, after, found := strings.Cut(c.language, "-"); found && after != "" {
		return strings.ToUpper(after)
	}
	return "US"
}

type Episode struct {
	ID          int64
	Number      int
	Title       string
	Overview    string
	ReleaseDate string
	StillPath   string
}

type Season struct {
	ID         int64
	PosterPath string
	Episodes   []Episode
}

func (c *Client) Season(ctx context.Context, showID int64, season int) (Season, error) {
	var response struct {
		ID         int64  `json:"id"`
		PosterPath string `json:"poster_path"`
		Episodes   []struct {
			ID            int64  `json:"id"`
			EpisodeNumber int    `json:"episode_number"`
			Name          string `json:"name"`
			Overview      string `json:"overview"`
			AirDate       string `json:"air_date"`
			StillPath     string `json:"still_path"`
		} `json:"episodes"`
	}
	path := "/tv/" + strconv.FormatInt(showID, 10) + "/season/" + strconv.Itoa(season)
	if err := c.get(ctx, path, nil, &response); err != nil {
		return Season{}, err
	}
	episodes := make([]Episode, 0, len(response.Episodes))
	for _, raw := range response.Episodes {
		episodes = append(episodes, Episode{
			ID: raw.ID, Number: raw.EpisodeNumber, Title: raw.Name, Overview: raw.Overview,
			ReleaseDate: raw.AirDate, StillPath: raw.StillPath,
		})
	}
	return Season{ID: response.ID, PosterPath: response.PosterPath, Episodes: episodes}, nil
}

type ImageCandidate struct {
	FilePath    string  `json:"file_path"`
	Language    string  `json:"iso_639_1,omitempty"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	AspectRatio float64 `json:"aspect_ratio,omitempty"`
	VoteAverage float64 `json:"vote_average,omitempty"`
	VoteCount   int     `json:"vote_count,omitempty"`
}

type Images struct {
	Posters   []ImageCandidate
	Backdrops []ImageCandidate
	Logos     []ImageCandidate
}

func (c *Client) Images(ctx context.Context, mediaType string, id int64) (Images, error) {
	if mediaType != "movie" && mediaType != "tv" {
		return Images{}, fmt.Errorf("unsupported TMDB media type %q", mediaType)
	}
	values := c.imageLanguages()
	var response struct {
		Posters   []ImageCandidate `json:"posters"`
		Backdrops []ImageCandidate `json:"backdrops"`
		Logos     []ImageCandidate `json:"logos"`
	}
	path := "/" + mediaType + "/" + strconv.FormatInt(id, 10) + "/images"
	if err := c.get(ctx, path, values, &response); err != nil {
		return Images{}, err
	}
	return Images{
		Posters: response.Posters, Backdrops: response.Backdrops, Logos: response.Logos,
	}, nil
}

func (c *Client) SeasonImages(ctx context.Context, showID int64, season int) ([]ImageCandidate, error) {
	var response struct {
		Posters []ImageCandidate `json:"posters"`
	}
	path := "/tv/" + strconv.FormatInt(showID, 10) + "/season/" + strconv.Itoa(season) + "/images"
	if err := c.get(ctx, path, c.imageLanguages(), &response); err != nil {
		return nil, err
	}
	return response.Posters, nil
}

func (c *Client) imageLanguages() url.Values {
	values := url.Values{}
	language := c.language
	if before, _, found := strings.Cut(language, "-"); found {
		language = before
	}
	if language != "" {
		values.Set("include_image_language", language+",null")
	}
	return values
}

func (c *Client) ImageURL(path string) string {
	return c.ImageURLSize(path, "original")
}

func (c *Client) ImageURLSize(path, size string) string {
	if path == "" {
		return ""
	}
	return c.imageURL + "/" + size + "/" + strings.TrimLeft(path, "/")
}

func (c *Client) get(ctx context.Context, path string, values url.Values, target any) error {
	if c.apiKey == "" {
		return fmt.Errorf("TMDB API key is not configured")
	}
	if values == nil {
		values = make(url.Values)
	}
	values.Set("api_key", c.apiKey)
	values.Set("language", c.language)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+values.Encode(), nil)
	if err != nil {
		return fmt.Errorf("create TMDB request: %w", err)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("TMDB request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &HTTPError{
			StatusCode: response.StatusCode,
			Status:     response.Status,
			Detail:     strings.TrimSpace(string(message)),
		}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode TMDB response: %w", err)
	}
	return nil
}

func dateYear(date string) int {
	if len(date) < 4 {
		return 0
	}
	year, _ := strconv.Atoi(date[:4])
	return year
}
