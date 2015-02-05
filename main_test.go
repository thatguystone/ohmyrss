package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"text/template"

	"github.com/bradfitz/gomemcache/memcache"
)

type TestVariables struct {
	TestURL         string
	CommonURL       string
	CommonURLAsPath string
}

const (
	testData = "test_data"
)

func TestMemcache(t *testing.T) {
	mc = memcache.New("127.0.0.1:11211")

	a := &article{
		Title:    "test title",
		FinalURL: "some other url",
		Content:  "i'm content with this",
	}

	cacheArticle("test", a, 180)

	got, err := hitCache("test")
	if err != nil {
		t.Fatalf("hitCache failed: %s", err)
	}

	if !reflect.DeepEqual(a, got) {
		t.Fatalf("not equal: %#v != %#v", a, got)
	}

	mc = nil
}

func setupServer(testName *string, testDir string) (*httptest.Server, func(string) (string, error)) {
	var server *httptest.Server

	templated := func(path string) (string, error) {
		t := template.New("test")

		tmpl, err := ioutil.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("error reading template file: %s", err)
		}

		t, err = t.Parse(string(tmpl))
		if err != nil {
			return "", err
		}

		var b bytes.Buffer
		err = t.Execute(&b, TestVariables{
			TestURL:         fmt.Sprintf("%s/%s/%s", server.URL, testDir, *testName),
			CommonURL:       fmt.Sprintf("%s/%s", server.URL, "_common"),
			CommonURLAsPath: url.QueryEscape(urlAsPath(fmt.Sprintf("%s/%s", server.URL, "_common"))),
		})
		if err != nil {
			return "", err
		}

		return strings.TrimSpace(b.String()), nil
	}

	server = httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := fmt.Sprintf("%s/%s",
				testData,
				strings.TrimLeft(r.URL.String(), "/"))

			res, err := templated(path)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}

			w.Write([]byte(res))
		}))

	return server, templated
}

func TestFeeds(t *testing.T) {
	var testName string
	testDir := "test_feeds"

	server, templated := setupServer(&testName, testDir)
	defer server.Close()

	files, err := ioutil.ReadDir(fmt.Sprintf("%s/%s", testData, testDir))
	if err != nil {
		t.Fatalf("could not read test_data: %s", err)
	}

	for _, f := range files {
		testName = f.Name()
		tc := fmt.Sprintf("%s/%s/%s/test", server.URL, testDir, testName)
		tr := fmt.Sprintf("%s/%s/%s/result", testData, testDir, testName)

		exp, err := templated(tr)
		if err != nil {
			t.Errorf("%s: error running template: %s", testName, err)
			continue
		}

		track := tracking{
			cid: 123,
		}

		got, redirectURL, err := handleFeed(tc, track)
		if err != nil {
			t.Errorf("%s: failed to handle feed: %s", testName, err)
			continue
		}

		if redirectURL != "" {
			if exp != redirectURL {
				t.Errorf("%s: wrong redirect URL, got:\n"+
					"	got:      %s\n"+
					"	expected: %s",
					testName,
					redirectURL,
					exp)
			}
		} else {
			if got != exp {
				t.Errorf("%s: output mismatch:\n"+
					"	got:      %s\n"+
					"	expected: %s",
					testName,
					got,
					exp)
			}
		}
	}
}

func TestRedirect(t *testing.T) {
	var testName string
	testDir := "test_redirect"

	server, templated := setupServer(&testName, testDir)
	defer server.Close()

	pubServer := httptest.NewServer(http.HandlerFunc(feedHandler))
	defer pubServer.Close()

	url := fmt.Sprintf("%s/?url=%s/%s/test", pubServer.URL, server.URL, testDir)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get error: %s", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("body read error: %s", err)
	}

	path := fmt.Sprintf("%s/%s/result", testData, testDir)
	exp, err := templated(path)
	if err != nil {
		t.Fatalf("error running template: %s", err)
	}

	if !strings.HasPrefix(string(body), exp) {
		t.Fatalf("%s: bad prefix:\n"+
			"	got:      %s\n"+
			"	expected: %s",
			testName,
			string(body),
			exp)
	}
}
