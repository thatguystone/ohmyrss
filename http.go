package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxRespBytes = 4 * 1024 * 1024
)

var (
	httpClient = &http.Client{
		Timeout: time.Second * 10,
	}

	errBadHost = errors.New("bad hostname")

	httpLocalDisabled = false
	disallowedNets    = []*net.IPNet{}
	disallowedCidrs   = []string{
		"127.0.0.0/8",
		"::1/128",
	}
)

func init() {
	for _, c := range disallowedCidrs {
		_, net, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		disallowedNets = append(disallowedNets, net)
	}
}

func httpDisableLocal() {
	httpLocalDisabled = true
}

func httpTestLocal(iu string) error {
	if !httpLocalDisabled {
		return nil
	}

	u, err := url.Parse(iu)
	if err != nil {
		return err
	}

	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		host = u.Host
	}

	addrs, err := net.LookupIP(host)
	if err != nil {
		return err
	}

	for _, a := range addrs {
		for _, n := range disallowedNets {
			if n.Contains(a) {
				return errBadHost
			}
		}
	}

	return nil
}

func httpGet(url string) (body io.ReadCloser, err error) {
	err = httpTestLocal(url)
	if err != nil {
		return
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		err = fmt.Errorf("could not create new request: %s", err)
		return
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		err = fmt.Errorf("could not load URL: %s", err)
		return
	}

	if resp.StatusCode != 200 {
		resp.Body.Close()
		resp.Body = nil
		err = fmt.Errorf("could not load URL: status code %d", resp.StatusCode)
		return
	}

	body = http.MaxBytesReader(nil, resp.Body, maxRespBytes)
	return
}

func httpGetRemoteIP(req *http.Request) string {
	if ips := req.Header.Get("X-Forwarded-For"); len(ips) > 0 {
		ipsa := strings.Split(ips, ",")
		return strings.TrimSpace(ipsa[0])
	}

	ip, _, _ := net.SplitHostPort(req.RemoteAddr)
	return ip
}
