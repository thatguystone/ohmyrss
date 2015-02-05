package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"math/rand"
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
	ServerHostPort string
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

		surl, _ := url.Parse(server.URL)
		sHostPort := surl.Host

		var b bytes.Buffer
		cpu, _ := url.Parse(fmt.Sprintf("%s/%s", server.URL, "_common"))
		err = t.Execute(&b, TestVariables{
			ServerHostPort: sHostPort,
			TestURL:         fmt.Sprintf("%s/%s/%s", server.URL, testDir, *testName),
			CommonURL:       fmt.Sprintf("%s/%s", server.URL, "_common"),
			CommonURLAsPath: url.QueryEscape(urlAsPath(*cpu)),
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

			var res []byte
			if strings.HasSuffix(path, ".jpg") {
				var err error
				res, err = ioutil.ReadFile(path)
				if err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
			} else {
				tmpl, err := templated(path)
				if err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				res = []byte(tmpl)
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

		uc, _ := url.Parse(tc)
		fr := feedRequest{
			baseURL: uc,
			t: tracking{
				cid: 123,
			},
		}

		got, redirectURL, err := handleFeed(fr)
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

func TestSchemelessURLs(t *testing.T) {
	var testName string
	testDir := "test_redirect"

	server, _ := setupServer(&testName, testDir)
	defer server.Close()

	pubServer := httptest.NewServer(http.HandlerFunc(feedHandler))
	defer pubServer.Close()

	u, _ := url.Parse(server.URL)

	addrs := []string{
		u.Host,
		server.URL,
	}

	for _, addr := range addrs {
		pu := fmt.Sprintf("%s/?url=%s/%s/test", pubServer.URL, addr, testDir)
		resp, err := http.Get(pu)
		if err != nil {
			t.Fatalf("get error: %s", err)
		}
		resp.Body.Close()

		if resp.StatusCode != 200 {
			t.Fatalf("request failed with code %d", resp.StatusCode)
		}
	}
}

func TestHTTPDisableLocal(t *testing.T) {
	httpDisableLocal()

	addrs := []string{
		"http://localhost",
		"http://localhost:8080",
		"http://127.0.0.1",
		"http://127.0.0.1:8080",
		"http://::1",
		"http://[::1]:8080",
	}

	for _, a := range addrs {
		_, err := httpGet(a)
		if err != errBadHost {
			t.Errorf("%s allowed to hit localhost, no good: %s", a, err)
		}
	}
}

func TestTracking(t *testing.T) {
	fr := feedRequest{
		t: tracking{
			ip:  "127.0.0.1",
			cid: rand.Uint32(),
			url: "http://localhost",
		},
	}

	url := getTrackingURL(fr, true)
	if !strings.Contains(url, "uip=127.0.0.1") {
		t.Fatalf("uip param not found in : %s", url)
	}

	url = getTrackingURL(fr, false)
	if strings.Contains(url, "uip=127.0.0.1") {
		t.Fatalf("uip param found in : %s", url)
	}
}

func TestHTTPGetRemoteIP(t *testing.T) {
	type req struct {
		forwardedFor string
		remoteAddr   string
		expect       string
	}

	reqs := []req{
		req{
			forwardedFor: "127.0.0.1",
			remoteAddr:   "192.168.1.2:123",
			expect:       "127.0.0.1",
		},
		req{
			forwardedFor: "127.0.0.1, abcd.no, wat",
			remoteAddr:   "192.168.1.2:124",
			expect:       "127.0.0.1",
		},
		req{
			forwardedFor: "   127.0.0.1, 192.168.1.2",
			remoteAddr:   "192.168.1.2:125",
			expect:       "127.0.0.1",
		},
		req{
			forwardedFor: "",
			remoteAddr:   "192.168.1.2:126",
			expect:       "192.168.1.2",
		},
	}

	for _, r := range reqs {
		req := &http.Request{
			RemoteAddr: r.remoteAddr,
			Header: http.Header{
				"X-Forwarded-For": []string{r.forwardedFor},
			},
		}

		ip := httpGetRemoteIP(req)
		if ip != r.expect {
			t.Errorf("wrong IP for %s: got %s", r.expect, ip)
		}
	}
}
