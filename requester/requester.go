// Copyright 2014 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package requester provides commands to run load tests and display results.
package requester

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"io/ioutil"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mohae/deepcopy"
	"golang.org/x/net/http2"
)

const megSenderUA = "meg/0.0.1"

type result struct {
	err           error
	statusCode    int
	duration      time.Duration
	connDuration  time.Duration // connection setup(DNS lookup + Dial up) duration
	dnsDuration   time.Duration // dns lookup duration
	reqDuration   time.Duration // request "write" duration
	resDuration   time.Duration // response "read" duration
	delayDuration time.Duration // delay between response and request
	contentLength int64
}

type Work struct {
	// Request is the request to be made.
	Request *http.Request

	//RequestBody []byte

	RequestParamSlice *RequestParamSlice

	DataType string

	DisableOutput bool

	// N is the total number of requests to make.
	N int

	// C is the concurrency level, the number of concurrent workers to run.
	C int

	// H2 is an option to make HTTP/2 requests
	H2 bool

	// Timeout in seconds.
	SingleRequestTimeout time.Duration
	// Timeout in seconds
	PerformanceTimeout time.Duration

	// Qps is the rate limit.
	QPS int

	// DisableCompression is an option to disable compression in response
	DisableCompression bool

	// DisableKeepAlives is an option to prevents re-use of TCP connections between different HTTP requests
	DisableKeepAlives bool

	// DisableRedirects is an option to prevent the following of HTTP redirects
	DisableRedirects bool

	// RandomInput is an option to enable random data for input when input file has multi rows
	RandomInput bool

	// send requests synchronous in single worker
	Async bool

	// Output represents the output type. If "csv" is provided, the
	// output will be dumped as a csv stream.
	Output string

	// ProxyAddr is the address of HTTP proxy server in the format on "host:port".
	// Optional.
	ProxyAddr *url.URL

	// Writer is where results will be written. If nil, results are written to stdout.
	Writer io.Writer

	results   chan *result
	stopCh    chan struct{}
	startTime time.Time

	report *report
}

func (b *Work) writer() io.Writer {
	if b.Writer == nil {
		return os.Stdout
	}
	return b.Writer
}

// Run makes all the requests, prints the summary. It blocks until
// all work is done.
func (b *Work) Run() {
	// append hey's user agent
	ua := b.Request.UserAgent()
	if ua == "" {
		ua = megSenderUA
	} else {
		ua += " " + megSenderUA
	}

	b.results = make(chan *result)
	b.stopCh = make(chan struct{}, b.C)
	b.startTime = time.Now()
	b.report = newReport(b.writer(), b.results, b.Output)
	b.report.start()

	b.runWorkers()
	b.Finish()
}

func (b *Work) Finish() {
	for i := 0; i < b.C; i++ {
		b.stopCh <- struct{}{}
	}
	close(b.results)
	b.results = nil

	b.report.stop()
}

func (b *Work) makeRequest(c *http.Client, p *RequestParam) {
	s := time.Now()
	var size int64
	var code int
	var dnsStart, connStart, resStart, reqStart, delayStart time.Time
	var dnsDuration, connDuration, resDuration, reqDuration, delayDuration time.Duration
	//req := cloneRequest(b.Request, b.RequestBody)
	req := cloneRequest(b.Request, p, b.DataType)
	trace := &httptrace.ClientTrace{
		DNSStart: func(info httptrace.DNSStartInfo) {
			dnsStart = time.Now()
		},
		DNSDone: func(dnsInfo httptrace.DNSDoneInfo) {
			dnsDuration = time.Now().Sub(dnsStart)
		},
		GetConn: func(h string) {
			connStart = time.Now()
		},
		GotConn: func(connInfo httptrace.GotConnInfo) {
			connDuration = time.Now().Sub(connStart)
			reqStart = time.Now()
		},
		WroteRequest: func(w httptrace.WroteRequestInfo) {
			reqDuration = time.Now().Sub(reqStart)
			delayStart = time.Now()
		},
		GotFirstResponseByte: func() {
			delayDuration = time.Now().Sub(delayStart)
			resStart = time.Now()
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := c.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err == nil {
		size = resp.ContentLength
		code = resp.StatusCode
		body := &bytes.Buffer{}
		if b.DisableOutput == false {
			_, err := body.ReadFrom(resp.Body)
			if err == nil {
				Info.Printf("%s\t%d\t%s\n", strings.TrimSpace(string(p.Content)), code, strings.TrimSpace(body.String()))
			} else {
				Error.Println(err)
				return
			}
		}
		io.Copy(ioutil.Discard, resp.Body)
	} else {
		Error.Println(err)
		return
	}
	t := time.Now()
	resDuration = t.Sub(resStart)
	finish := t.Sub(s)

	select {
	case b.results <- &result{
		statusCode:    code,
		duration:      finish,
		err:           err,
		contentLength: size,
		connDuration:  connDuration,
		dnsDuration:   dnsDuration,
		reqDuration:   reqDuration,
		resDuration:   resDuration,
		delayDuration: delayDuration,
	}:
	default:
	}
}

// @param n	count to send
func (b *Work) runWorker(n int, widx int) {
	var throttle <-chan time.Time
	if b.QPS > 0 {
		throttle = time.Tick(time.Duration((1e6/(b.QPS))*b.C) * time.Microsecond)
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		DisableCompression: b.DisableCompression,
		DisableKeepAlives:  b.DisableKeepAlives,
		Proxy:              http.ProxyURL(b.ProxyAddr),
	}
	if b.H2 {
		http2.ConfigureTransport(tr)
	} else {
		tr.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	}

	client := &http.Client{Transport: tr, Timeout: b.SingleRequestTimeout}
	if b.DisableRedirects {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	if b.Async {
		// async
		cli := deepcopy.Copy(*client)
		cliObj, ok := cli.(http.Client)
		if ok {
			if b.PerformanceTimeout > 0 {
				b.asyncSend(throttle, cliObj)
			} else {
				b.asyncSendN(widx, n, throttle, cliObj)
			}
		}
	} else {
		// sync
		cli := deepcopy.Copy(*client)
		cliObj, ok := cli.(http.Client)
		if ok {
			if b.PerformanceTimeout > 0 {
				b.syncSend(throttle, cliObj)
			} else {
				b.syncSendN(widx, n, throttle, cliObj)
			}
		}
	}
}

// sync send n
func (b *Work) syncSendN(widx int, n int, throttle <-chan time.Time, client http.Client) {
	for i := 0; i < n; i++ {
		if b.QPS > 0 {
			<-throttle
		}
		select {
		case <-b.stopCh:
			break
		default:
			requestParam := b.getRequestParam(i*b.C + widx)
			b.makeRequest(&client, &requestParam)
		}
	}
}

// sync send
func (b *Work) syncSend(throttle <-chan time.Time, client http.Client) {
	for i := 0; ; i++ {
		if time.Now().Sub(b.startTime) > b.PerformanceTimeout {
			break
		}
		if b.QPS > 0 {
			<-throttle
		}
		select {
		case <-b.stopCh:
			break
		default:
			requestParam := b.getRequestParam(i)
			b.makeRequest(&client, &requestParam)
		}
	}
}

// async send by count
func (b *Work) asyncSendN(widx int, n int, throttle <-chan time.Time, client http.Client) {
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		if b.QPS > 0 {
			<-throttle
		}
		go func() {
			select {
			case <-b.stopCh:
				break
			default:
				requestParam := b.getRequestParam(i*b.C + widx)
				b.makeRequest(&client, &requestParam)
			}
			wg.Done()
		}()
	}
	wg.Wait()
}

// async send by time
func (b *Work) asyncSend(throttle <-chan time.Time, client http.Client) {
	var wg sync.WaitGroup
	for i := 0; ; i++ {
		if time.Now().Sub(b.startTime) > b.PerformanceTimeout {
			break
		}
		wg.Add(1)
		if b.QPS > 0 {
			<-throttle
		}
		go func() {
			select {
			case <-b.stopCh:
				break
			default:
				requestParam := b.getRequestParam(i)
				b.makeRequest(&client, &requestParam)
			}
			wg.Done()
		}()
	}
	wg.Wait()
}

func (b *Work) getRequestParam(idx int) RequestParam {
	length := len(b.RequestParamSlice.RequestParams)
	if length > 0 {
		if b.RandomInput {
			return b.RequestParamSlice.RequestParams[rand.Intn(length)]
		} else {
			return b.RequestParamSlice.RequestParams[(idx)%length]
		}
	} else {
		return RequestParam{
			Content: []byte(""),
		}
	}
}

func (b *Work) runWorkers() {
	var wg sync.WaitGroup
	wg.Add(b.C)

	for i := 0; i < b.C; i++ {
		go func(i int) {
			b.runWorker(b.N/(b.C), i)
			defer wg.Done()
		}(i)
	}
	wg.Wait()
}

/**
// cloneRequest returns a clone of the provided *http.Request.
// The clone is a shallow copy of the struct and its Header map.
func cloneRequest(r *http.Request, body []byte) *http.Request {
	// shallow copy of the struct
	r2 := new(http.Request)
	*r2 = *r
	// deep copy of the Header
	r2.Header = make(http.Header, len(r.Header))
	for k, s := range r.Header {
		r2.Header[k] = append([]string(nil), s...)
	}
	if len(body) > 0 {
		r2.Body = ioutil.NopCloser(bytes.NewReader(body))
	}
	return r2
}
*/

func cloneRequest(r *http.Request, p *RequestParam, t string) *http.Request {
	// shallow copy of the struct
	r2 := new(http.Request)
	*r2 = *r
	// deep copy of the Header
	r2.Header = make(http.Header, len(r.Header))
	for k, s := range r.Header {
		r2.Header[k] = append([]string(nil), s...)
	}
	if strings.ToUpper(t) == "JSON" {
		r2.Body = ioutil.NopCloser(bytes.NewReader(p.Content))
	} else if strings.ToUpper(t) == "FORM" {
		var obj map[string]string
		err := json.Unmarshal([]byte(p.Content), &obj)
		if err != nil {
			Error.Println(err)
			return nil
		}
		filesMap := make(map[string]string)
		dataMap := make(map[string]string)
		for key, val := range obj {
			startWithAt := strings.HasPrefix(val, "@")
			if startWithAt == true {
				filesMap[key] = val[1:]
			} else {
				dataMap[key] = val
			}
		}

		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		if len(filesMap) != 0 {
			for key, path := range filesMap {
				file, err := os.Open(path)
				if err != nil {
					Error.Println(err)
					continue
				}
				defer file.Close()

				part, err := writer.CreateFormFile(key, path)
				if err != nil {
					Error.Println(err)
					continue
				}
				_, err = io.Copy(part, file)
			}
		}
		if len(dataMap) != 0 {
			for key, val := range dataMap {
				_ = writer.WriteField(key, val)
			}
		}
		writer.Close()

		/**
		req, err := http.NewRequest("POST", r.URL.String(), body)
		if err != nil {
			log.Fatal(err.Error())
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())
			return req
		*/

		//r2.Body = ioutil.NopCloser(bytes.NewReader(body.Bytes()))
		r2.Body = ioutil.NopCloser(bytes.NewReader(body.Bytes()))
		r2.ContentLength = int64(len(body.Bytes()))
		r2.Header.Set("Content-Type", writer.FormDataContentType())
	} else {
		r2.Body = ioutil.NopCloser(bytes.NewReader(p.Content))
	}

	return r2
}

func init() {
}
