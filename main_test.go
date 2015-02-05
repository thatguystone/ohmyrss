package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"text/template"

	"github.com/bradfitz/gomemcache/memcache"
)

type TestVariables struct {
	TestURL   string
	CommonURL string
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

func TestFeeds(t *testing.T) {
	var server *httptest.Server
	var testName string

	templated := func(tmpl []byte) (string, error) {
		t := template.New("test")

		t, err := t.Parse(string(tmpl))
		if err != nil {
			return "", err
		}

		var b bytes.Buffer
		err = t.Execute(&b, TestVariables{
			TestURL:   fmt.Sprintf("%s/%s", server.URL, testName),
			CommonURL: fmt.Sprintf("%s/%s", server.URL, "_common"),
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

			tmpl, err := ioutil.ReadFile(path)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}

			res, err := templated(tmpl)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}

			w.Write([]byte(res))
		}))
	defer server.Close()

	files, err := ioutil.ReadDir(testData)
	if err != nil {
		t.Fatalf("could not read test_data: %s", err)
	}

	for _, f := range files {
		testName = f.Name()
		if testName == "_common" {
			continue
		}

		tc := fmt.Sprintf("%s/%s/test", server.URL, testName)
		tr := fmt.Sprintf("%s/%s/result", testData, testName)

		expb, err := ioutil.ReadFile(tr)
		if err != nil {
			t.Errorf("%s: error reading result file: %s", testName, err)
			continue
		}

		exp, err := templated(expb)
		if err != nil {
			t.Errorf("%s: error running template: %s", testName, err)
			continue
		}

		got, redirectURL, err := handleFeed(tc)
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

}
