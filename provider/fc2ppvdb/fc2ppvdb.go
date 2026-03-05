package fc2ppvdb

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/gocolly/colly/v2"
	"golang.org/x/text/language"

	"github.com/metatube-community/metatube-sdk-go/common/parser"
	"github.com/metatube-community/metatube-sdk-go/model"
	"github.com/metatube-community/metatube-sdk-go/provider"
	"github.com/metatube-community/metatube-sdk-go/provider/fc2/fc2util"
	"github.com/metatube-community/metatube-sdk-go/provider/internal/scraper"
)

var (
	_ provider.MovieProvider = (*FC2PPVDB)(nil)
	_ provider.ConfigSetter  = (*FC2PPVDB)(nil)
)

const (
	Name     = "FC2PPVDB"
	Priority = 1000 - 2
)

const (
	baseURL       = "https://fc2ppvdb.com/"
	movieURL      = "https://fc2ppvdb.com/articles/%s"
	articleAPIURL = "https://fc2ppvdb.com/articles/article-info?videoid=%s"
)

// articleResponse is the JSON response from the FC2PPVDB article API.
type articleResponse struct {
	IsLoggedIn int `json:"isLoggedIn"`
	Article    *struct {
		ID          int    `json:"id"`
		Title       string `json:"title"`
		VideoID     int    `json:"video_id"`
		ReleaseDate string `json:"release_date"`
		Duration    string `json:"duration"`
		ImageURL    string `json:"image_url"`
		Writer      *struct {
			Name string `json:"name"`
		} `json:"writer"`
		Actresses []struct {
			Name string `json:"name"`
		} `json:"actresses"`
		Tags []struct {
			Name string `json:"name"`
		} `json:"tags"`
	} `json:"article"`
}

type FC2PPVDB struct {
	*scraper.Scraper
}

func New() *FC2PPVDB {
	return &FC2PPVDB{scraper.NewDefaultScraper(Name, baseURL, Priority, language.Japanese)}
}

func (fc2ppvdb *FC2PPVDB) SetConfig(config provider.Config) error {
	if config.Has("xsrf_token") && config.Has("session") {
		xsrf, err := config.GetString("xsrf_token")
		if err != nil {
			return err
		}
		session, err := config.GetString("session")
		if err != nil {
			return err
		}
		return fc2ppvdb.SetCookies(baseURL, []*http.Cookie{
			{Name: "XSRF-TOKEN", Value: xsrf},
			{Name: "fc2ppvdb_session", Value: session},
		})
	}
	return nil
}

func (fc2ppvdb *FC2PPVDB) NormalizeMovieID(id string) string {
	return fc2util.ParseNumber(id)
}

func (fc2ppvdb *FC2PPVDB) GetMovieInfoByID(id string) (info *model.MovieInfo, err error) {
	return fc2ppvdb.GetMovieInfoByURL(fmt.Sprintf(movieURL, id))
}

func (fc2ppvdb *FC2PPVDB) ParseMovieIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return path.Base(homepage.Path), nil
}

func (fc2ppvdb *FC2PPVDB) GetMovieInfoByURL(rawURL string) (info *model.MovieInfo, err error) {
	id, err := fc2ppvdb.ParseMovieIDFromURL(rawURL)
	if err != nil {
		return
	}

	info = &model.MovieInfo{
		ID:            id,
		Number:        fmt.Sprintf("FC2-%s", id),
		Provider:      fc2ppvdb.Name(),
		Homepage:      rawURL,
		Actors:        []string{},
		PreviewImages: []string{},
		Genres:        []string{},
	}

	c := fc2ppvdb.ClonedCollector()

	scraper.SetupHTTPErrorHandling(c, &err)

	// Parse JSON API response
	c.OnResponse(func(r *colly.Response) {
		if err != nil {
			return
		}

		var resp articleResponse
		if jsonErr := json.Unmarshal(r.Body, &resp); jsonErr != nil {
			err = fmt.Errorf("fc2ppvdb: failed to parse JSON: %w", jsonErr)
			return
		}

		if resp.Article == nil {
			err = provider.ErrInfoNotFound
			return
		}

		a := resp.Article
		info.Title = strings.TrimSpace(a.Title)
		if info.Title == "" {
			err = provider.ErrInfoNotFound
			return
		}

		info.ID = fmt.Sprintf("%d", a.VideoID)
		info.Number = fmt.Sprintf("FC2-%d", a.VideoID)

		if a.ImageURL != "" {
			info.CoverURL = a.ImageURL
		}

		if a.Writer != nil && a.Writer.Name != "" {
			info.Maker = a.Writer.Name
		}

		for _, actress := range a.Actresses {
			if name := strings.TrimSpace(actress.Name); name != "" {
				info.Actors = append(info.Actors, name)
			}
		}

		for _, tag := range a.Tags {
			if name := strings.TrimSpace(tag.Name); name != "" {
				info.Genres = append(info.Genres, name)
			}
		}

		if a.ReleaseDate != "" {
			info.ReleaseDate = parser.ParseDate(a.ReleaseDate)
		}

		if a.Duration != "" {
			info.Runtime = parser.ParseRuntime(a.Duration)
		}
	})

	apiURL := fmt.Sprintf(articleAPIURL, id)
	if vErr := c.Visit(apiURL); vErr != nil {
		err = vErr
	}
	return
}

func init() {
	provider.Register(Name, New)
}
