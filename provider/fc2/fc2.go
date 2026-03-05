package fc2

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/gocolly/colly/v2"
	"golang.org/x/text/language"

	"github.com/metatube-community/metatube-sdk-go/common/parser"
	"github.com/metatube-community/metatube-sdk-go/model"
	"github.com/metatube-community/metatube-sdk-go/provider"
	"github.com/metatube-community/metatube-sdk-go/provider/fc2/fc2util"
	"github.com/metatube-community/metatube-sdk-go/provider/internal/scraper"
)

var (
	_ provider.MovieProvider = (*FC2)(nil)
	_ provider.ConfigSetter  = (*FC2)(nil)
)

const (
	Name     = "FC2"
	Priority = 1000
)

const (
	baseURL   = "https://adult.contents.fc2.com/"
	movieURL  = "https://adult.contents.fc2.com/article/%s/"
	sampleURL = "https://adult.contents.fc2.com/api/v2/videos/%s/sample"
)

type FC2 struct {
	*scraper.Scraper
}

func New() *FC2 {
	return &FC2{scraper.NewDefaultScraper(Name, baseURL, Priority, language.Japanese)}
}

func (fc2 *FC2) SetConfig(config provider.Config) error {
	if !config.Has("session_id") {
		return nil
	}
	sessionID, err := config.GetString("session_id")
	if err != nil {
		return err
	}

	// PHPSESSID is the content session cookie (on .contents.fc2.com)
	contentsCookies := []*http.Cookie{
		{Name: "CONTENTS_FC2_PHPSESSID", Value: sessionID},
	}

	// Cookies on .fc2.com (Cloudflare clearance + FC2 auth)
	var fc2Cookies []*http.Cookie
	for _, key := range []string{"cf_clearance", "fcu", "fcus", "FC2_GDPR", "contents_mode", "contents_func_mode"} {
		if config.Has(key) {
			v, _ := config.GetString(key)
			if v != "" {
				fc2Cookies = append(fc2Cookies, &http.Cookie{Name: key, Value: v})
			}
		}
	}

	if err := fc2.SetCookies(baseURL, contentsCookies); err != nil {
		return err
	}
	if len(fc2Cookies) > 0 {
		// Set cookies on the parent domain too for Cloudflare + auth
		if err := fc2.SetCookies("https://fc2.com/", fc2Cookies); err != nil {
			return err
		}
	}

	// Cloudflare validates that the User-Agent matches the cf_clearance cookie.
	if config.Has("user_agent") {
		v, _ := config.GetString("user_agent")
		if v != "" {
			fc2.SetUserAgent(v)
		}
	}
	return nil
}

func (fc2 *FC2) NormalizeMovieID(id string) string {
	return fc2util.ParseNumber(id)
}

func (fc2 *FC2) GetMovieInfoByID(id string) (info *model.MovieInfo, err error) {
	return fc2.GetMovieInfoByURL(fmt.Sprintf(movieURL, id))
}

func (fc2 *FC2) ParseMovieIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return path.Base(homepage.Path), nil
}

func (fc2 *FC2) GetMovieInfoByURL(rawURL string) (info *model.MovieInfo, err error) {
	id, err := fc2.ParseMovieIDFromURL(rawURL)
	if err != nil {
		return
	}

	info = &model.MovieInfo{
		ID:            id,
		Number:        fmt.Sprintf("FC2-%s", id),
		Provider:      fc2.Name(),
		Homepage:      rawURL,
		Actors:        []string{},
		PreviewImages: []string{},
		Genres:        []string{},
	}

	c := fc2.ClonedCollector()

	scraper.SetupHTTPErrorHandling(c, &err)

	// Headers
	c.OnXML(`//div[@class="items_article_headerInfo"]`, func(e *colly.XMLElement) {
		// Modified title extraction
		rawTitle := e.ChildText(`./h3`) // Get all text content from h3 element

		// Process as HTML
		doc, err := goquery.NewDocumentFromReader(strings.NewReader("<h3>" + rawTitle + "</h3>"))
		if err == nil {
			// Remove spam-like spans
			doc.Find("span").Each(func(i int, s *goquery.Selection) {
				style, exists := s.Attr("style")
				if exists && (strings.Contains(style, "zoom:0.01") ||
					strings.Contains(style, "display:none") ||
					strings.Contains(style, "overflow:hidden")) {
					s.Remove()
				}
			})

			// Get clean text
			info.Title = strings.TrimSpace(doc.Text())
		} else {
			// Fallback: Remove spam patterns using regex
			pattern := regexp.MustCompile(`\*+[a-z0-9*]+\s*`)
			cleanTitle := pattern.ReplaceAllString(rawTitle, "")
			info.Title = strings.TrimSpace(cleanTitle)
		}
		info.Genres = e.ChildTexts(`.//section[@class="items_article_TagArea"]/div/a`)
		info.Maker = e.ChildText(`.//ul/li[last()]/a`)
		{ /* score */
			class := e.ChildAttr(`.//li[@class="items_article_StarA"]/a/p/span`, "class")
			info.Score = parser.ParseScore(regexp.MustCompile(`(\d+)$`).FindString(class))
		}
		{ /* release date */
			ss := strings.Split(e.ChildText(`.//div[@class="items_article_Releasedate"]/p`), ":")
			info.ReleaseDate = parser.ParseDate(ss[len(ss)-1])
		}
	})

	// Extra Info
	c.OnXML(`//div[@class="items_article_headerInfo"]/div[@class="items_article_softDevice"]/p`, func(e *colly.XMLElement) {
		key, value, found := strings.Cut(e.Text, ":")
		if !found {
			return
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "Sale Day", "販売日":
			info.ReleaseDate = parser.ParseDate(value)
		case "Product ID", "商品ID":
			// Fallback only:
			if productID := fc2util.ParseNumber(value); productID != id {
				// info.ID = productID
				// info.Number = fmt.Sprintf("FC2-%s", productID)
				err = fmt.Errorf("ID mismatch: FC2-%s != FC2-%s", id, productID)
			}
		}
	})

	// Summary
	c.OnXML(`//section[@class="items_article_Contents"]/iframe`, func(e *colly.XMLElement) {
		d := c.Clone()
		d.OnXML(`//html/body/div`, func(e *colly.XMLElement) {
			info.Summary = strings.TrimSpace(e.Text)
		})
		d.Visit(e.Request.AbsoluteURL(e.Attr("src")))
	})

	// Thumb
	c.OnXML(`//div[@class="items_article_MainitemThumb"]/span/img`, func(e *colly.XMLElement) {
		info.ThumbURL = e.Request.AbsoluteURL(e.Attr("src"))
	})

	// Runtime
	c.OnXML(`//div[@class="items_article_MainitemThumb"]//p[@class="items_article_info"]`, func(e *colly.XMLElement) {
		info.Runtime = parser.ParseRuntime(e.Text)
	})

	// Preview Images
	c.OnXML(`//section[@class="items_article_SampleImages"]/ul/li`, func(e *colly.XMLElement) {
		info.PreviewImages = append(info.PreviewImages, e.Request.AbsoluteURL(e.ChildAttr(`.//a`, "href")))
	})

	// Cover (fallbacks)
	c.OnScraped(func(_ *colly.Response) {
		if err == nil && info.Title == "" {
			err = provider.ErrInfoNotFound
			return
		}
		if info.ThumbURL != "" {
			info.CoverURL = info.ThumbURL
		} else if len(info.PreviewImages) > 0 {
			// Use the first preview image as cover due to
			// thumb image's poor resolution.
			info.CoverURL = info.PreviewImages[0]
		}
	})

	// Preview Video
	//c.OnScraped(func(r *colly.Response) {
	//	d := c.Clone()
	//	d.OnResponse(func(r *colly.Response) {
	//		data := struct {
	//			Path string `json:"path"`
	//			Code int    `json:"code"`
	//		}{}
	//		if err := json.Unmarshal(r.Body, &data); err == nil && data.Code == http.StatusOK {
	//			info.PreviewVideoURL = data.Path
	//		}
	//	})
	//	d.Visit(fmt.Sprintf(sampleURL, info.ID))
	//})

	if vErr := c.Visit(info.Homepage); vErr != nil {
		err = vErr
	}
	return
}

func init() {
	provider.Register(Name, New)
}
