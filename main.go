package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/gob"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/http/fcgi"
	"net/url"
	"strings"

	"code.google.com/p/cascadia"
	"github.com/PuerkitoBio/goquery"
	"github.com/bkaradzic/go-lz4"
	"github.com/bradfitz/gomemcache/memcache"
	"github.com/thatguystone/swan"
)

type article struct {
	Title    string
	FinalURL string
	Content  string
}

type tracking struct {
	cid   uint32
	url   string
	title string
}

var (
	runFcgi         = false
	httpPort        = 8080
	memcacheServers = ""

	errNoMc        = errors.New("memcache disabled")
	errInvalidPage = errors.New("could not find a feed on this page")

	linkAlt = cascadia.MustCompile(
		"link[rel=alternate][type=\"application/rss+xml\"][href], " +
			"link[rel=alternate][type=\"application/atom+xml\"][href]")

	mc *memcache.Client
)

func init() {
	flag.BoolVar(&runFcgi, "fcgi", false, "run as a fastcgi server")
	flag.IntVar(&httpPort, "httpPort", 8080, "run a debug server")
	flag.StringVar(&memcacheServers, "mcServers", "", "comma-separated list of memcache servers")
}

func main() {
	flag.Parse()

	if memcacheServers != "" {
		mc = memcache.New(strings.Split(memcacheServers, ",")...)
	}

	http.HandleFunc("/", feedHandler)

	if runFcgi {
		fcgi.Serve(nil, nil)
	} else {
		http.ListenAndServe(fmt.Sprintf(":%d", httpPort), nil)
	}
}

func hitCache(key string) (*article, error) {
	if mc == nil {
		return nil, errNoMc
	}

	item, err := mc.Get(key)
	if err != nil {
		return nil, err
	}

	lzdc, err := lz4.Decode(nil, item.Value)
	if err != nil {
		return nil, err
	}

	var b bytes.Buffer
	var a *article

	dec := gob.NewDecoder(&b)
	b.Write(lzdc)
	err = dec.Decode(&a)

	return a, err
}

func cacheArticle(key string, a *article, expire int32) {
	if mc == nil {
		return
	}

	var b []byte
	if a != nil {
		var bf bytes.Buffer
		enc := gob.NewEncoder(&bf)
		err := enc.Encode(a)
		if err != nil {
			return
		}

		b, err = lz4.Encode(nil, bf.Bytes())
		if err != nil {
			return
		}
	}

	mc.Set(&memcache.Item{
		Key:        key,
		Value:      b,
		Expiration: expire,
	})
}

func getArticle(url string) *article {
	if url == "" {
		return nil
	}

	sum := sha1.Sum([]byte(url))
	key := "ohmyrss_" + base64.StdEncoding.EncodeToString(sum[:])

	art, err := hitCache(key)
	if err == nil {
		return art
	}

	sa, err := swan.FromURL(url)
	cacheTime := int32(60 * 3)
	if err == nil {
		cacheTime = 60 * 60 * 24 * 7
	}

	if sa != nil && sa.TopNode != nil {
		html, _ := sa.TopNode.Html()
		art = &article{
			Title:    sa.Meta.Title,
			FinalURL: sa.URL,
			Content:  strings.TrimSpace(html),
		}
	}

	cacheArticle(key, art, cacheTime)
	return art
}

func feedHandler(w http.ResponseWriter, req *http.Request) {
	req.Body.Close()

	feedURL := req.FormValue("url")
	if feedURL == "" {
		http.Error(w, "missing url parameter", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(feedURL, "http://") &&
		!strings.HasPrefix(feedURL, "https://") {

		feedURL = "http://" + feedURL
	}

	t := tracking{
		cid: rand.Uint32(),
	}

	feed, redirectURL, err := handleFeed(feedURL, t)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if redirectURL != "" {
		req.URL.RawQuery = fmt.Sprintf("url=%s", redirectURL)
		http.Redirect(w, req, req.URL.String(), http.StatusMovedPermanently)
		return
	}

	w.Header().Add("Content-Type", "text/xml; charset=utf-8")
	w.Write([]byte(feed))
}

func handleFeed(url string, t tracking) (feed string, redirectURL string, err error) {
	body, _, err := swan.HTTPGet(url)
	if err != nil {
		return
	}
	defer body.Close()

	in, err := ioutil.ReadAll(body)
	if err != nil {
		return
	}

	t.url = url

	var rss Rss
	err = xml.Unmarshal(in, &rss)
	if err == nil {
		feed, err = handleRss(&rss, t)
		return
	}

	var atom Atom
	err = xml.Unmarshal(in, &atom)
	if err == nil {
		feed, err = handleAtom(&atom, t)
		return
	}

	redirectURL = checkLandingPage(url, string(in))
	if redirectURL != "" {
		err = nil
	} else {
		err = errInvalidPage
	}

	return
}

func handleRss(rss *Rss, t tracking) (string, error) {
	t.title = rss.Channel.Title
	track(t)

	for _, item := range rss.Channel.Items {
		a := getArticle(item.Link)

		// Don't modify if something went wrong
		if a == nil {
			continue
		}

		if a.Title != "" {
			item.Title = a.Title
		}

		if a.FinalURL != "" {
			item.Link = a.FinalURL
		}

		if a.Content != "" {
			item.Description = a.Content
		}

		t.title = item.Title
		t.url = item.Link
		addTracking(&item.Description, t)
	}

	return xmlEncode(rss)
}

func handleAtom(atom *Atom, t tracking) (string, error) {
	t.title = atom.Title
	track(t)

	for _, item := range atom.Entries {
		if item.Link == nil {
			continue
		}

		a := getArticle(item.Link.Href)

		// Don't modify if something went wrong
		if a == nil {
			continue
		}

		if a.Title != "" {
			item.Title = a.Title
		}

		if a.FinalURL != "" {
			item.Link.Href = a.FinalURL
		}

		if a.Content != "" {
			if item.Content == nil {
				item.Content = &AtomContent{}
			}

			item.Content.Type = "html"
			item.Content.Content = a.Content
		}

		t.title = item.Title
		t.url = item.Link.Href
		addTracking(&item.Content.Content, t)
	}

	return xmlEncode(atom)
}

func xmlEncode(v interface{}) (string, error) {
	res, err := xml.Marshal(v)
	if err != nil {
		return "", err
	}

	return string(res), nil
}

func urlAsPath(u string) string {
	up, err := url.Parse(u)
	if err != nil {
		return "/url-parse-error"
	}

	up.Scheme = ""
	up.Opaque = ""
	up.User = nil

	return strings.TrimPrefix(up.String(), "/")
}

func getTrackingURL(t tracking) string {
	return fmt.Sprintf(
		"https://www.google-analytics.com/collect?v=1&tid=UA-6408039-10&cid=%d&t=pageview&dh=ohmyrss.com&dp=%s&dt=%s",
		t.cid,
		url.QueryEscape(urlAsPath(t.url)),
		url.QueryEscape(t.title))
}

func track(t tracking) {
	go func() {
		body, _, err := swan.HTTPGet(getTrackingURL(t))
		if err == nil {
			body.Close()
		}
	}()
}

func addTracking(content *string, t tracking) {
	*content += fmt.Sprintf("<img src=\"%s\"/>", getTrackingURL(t))
}

func checkLandingPage(purl string, content string) (redirectURL string) {
	// Well, maybe we're looking at a landing page...
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(content))
	if err != nil {
		return
	}

	feedHref, _ := doc.FindMatcher(linkAlt).Attr("href")
	if feedHref == "" {
		return
	}

	feedURL, err := url.Parse(feedHref)
	if err != nil {
		return
	}

	// If this causes an error, an earlier check failed
	baseURL, _ := url.Parse(purl)

	redirectURL = baseURL.ResolveReference(feedURL).String()
	return
}
