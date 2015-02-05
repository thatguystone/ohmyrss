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
	"net/http"
	"net/http/fcgi"
	"net/url"
	"strings"

	"github.com/bkaradzic/go-lz4"
	"github.com/bradfitz/gomemcache/memcache"
	"github.com/thatguystone/swan"
)

type article struct {
	Title    string
	FinalURL string
	Content  string
}

var (
	runFcgi         = false
	httpPort        = 8080
	memcacheServers = ""

	errNoMc = errors.New("memcache disabled")

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

	feedURL, err := getFeedURL(req.URL)
	if err != nil {
		http.Error(w, "invalid feed URL", 400)
		return
	}

	feed, err := handleFeed(feedURL)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	w.Write([]byte(feed))
}

func getFeedURL(origURL *url.URL) (string, error) {
	origURL.Scheme = ""
	origURL.User = nil
	origURL.Host = ""

	url, err := url.Parse(strings.TrimLeft(origURL.String(), "/"))
	if err != nil {
		return "", err
	}

	if url.Scheme == "" {
		url.Scheme = "http"
	}

	return url.String(), nil
}

func handleFeed(url string) (string, error) {
	body, _, err := swan.HttpGet(url)
	if err != nil {
		return "", err
	}
	defer body.Close()

	in, err := ioutil.ReadAll(body)
	if err != nil {
		return "", err
	}

	var rss Rss
	err = xml.Unmarshal(in, &rss)
	if err == nil {
		return handleRss(&rss)
	}

	var atom Atom
	err = xml.Unmarshal(in, &atom)
	if err == nil {
		return handleAtom(&atom)
	}

	return "", err
}

func handleRss(rss *Rss) (string, error) {
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
	}

	return xmlEncode(rss)
}

func handleAtom(atom *Atom) (string, error) {
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
