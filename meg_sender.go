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

// Command meg_sender is an HTTP load generator.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	gourl "net/url"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/alex19861108/meg-sender/requester"
)

const (
	headerRegexp = `^([\w-]+):\s*(.+)`
	authRegexp   = `^(.+):([^\s].+)`
)

var (
	m           = flag.String("m", "GET", "")
	headers     = flag.String("h", "", "")
	body        = flag.String("d", "", "")
	bodyFile    = flag.String("D", "", "")
	accept      = flag.String("A", "", "")
	contentType = flag.String("C", "text/html", "")
	authHeader  = flag.String("a", "", "")
	hostHeader  = flag.String("host", "", "")
	dataType    = flag.String("f", "TEXT", "")
	output      = flag.String("o", "", "")

	qps = flag.Int("qps", 0, "")
	c   = flag.Int("c", 50, "")
	n   = flag.Int("n", 0, "")
	t   = flag.Int("t", 0, "")
	T   = flag.Int("T", 60, "")

	h2   = flag.Bool("h2", false, "")
	cpus = flag.Int("cpus", runtime.GOMAXPROCS(-1), "")

	disableCompression = flag.Bool("disable-compression", false, "")
	disableKeepAlives  = flag.Bool("disable-keepalive", false, "")
	disableRedirects   = flag.Bool("disable-redirects", false, "")
	disableOutput      = flag.Bool("disable-output", false, "")
	randomInput        = flag.Bool("random-input", false, "")
	async              = flag.Bool("async", false, "")
	proxyAddr          = flag.String("x", "", "")
)

var usage = `Usage: meg_sender [options...] <url>

Options:
  -m    HTTP method, one of GET, POST, PUT, DELETE, HEAD, OPTIONS. Default is [GET].
  -qps  Rate limit, in seconds (QPS). If not set, send request one by one.
  -n    Number of requests to run. Default is [0].
  -t    Timeout for all request in seconds. Default is [0].

  -f    POST data type, one of TEXT, JSON, FORM, OPTIONS. Default is [TEXT]

  -c    Number of concurrent workers to run. Total number of requests cannot
        be smaller than the concurrency level. Default is [50].
  -H    Custom HTTP header. You can specify as many as needed by repeating the flag.
        For example, -H "Accept: text/html" -H "Content-Type: application/xml" .
  -T    Timeout for each request in seconds. Default is 60, use 0 for infinite.
  -A    HTTP Accept header.
  -d    HTTP request body.
  -D    HTTP request body from file. For example, /home/user/file.txt or ./file.txt.
  -C    Content-type, defaults to "text/html".
  -a    Basic authentication, username:password.
  -x    HTTP Proxy address as host:port.
  -h2   Enable HTTP/2.
  -o    Output type. If none provided, a summary is printed.
        "csv" is the only supported alternative. Dumps the response
        metrics in comma-separated values format.

  -host                 HTTP Host header.
  -cpus                 Number of used cpu cores.
                        (default for current machine is %d cores)

  -disable-compression  Disable compression.
  -disable-keepalive    Disable keep-alive, prevents re-use of TCP
                        connections between different HTTP requests.
  -disable-redirects    Disable following of HTTP redirects
  -disable-output       Disable response output.
  -random-input         Enable random input when input has multi rows.
  -async                Enable send requests asynchronously in single worker.

  -more                 Provides information on DNS lookup, dialup, request and
                        response timings.
`

func main() {
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, fmt.Sprintf(usage, runtime.NumCPU()))
	}

	var hs headerSlice
	flag.Var(&hs, "H", "")

	flag.Parse()
	if flag.NArg() < 1 {
		usageAndExit("")
	}

	runtime.GOMAXPROCS(*cpus)
	num := *n
	conc := *c
	qps := *qps

	if conc <= 0 {
		usageAndExit("-c cannot be smaller than 1.")
	}

	if *t > 0 {
		num = math.MaxInt32
		if num <= conc {
			usageAndExit("-c cannot be smaller than 1.")
		}
	} else {
		if num <= 0 || conc <= 0 {
			usageAndExit("-n and -c cannot be smaller than 1.")
		}

		if num < conc {
			usageAndExit("-n cannot be less than -c.")
		}
	}

	if *async && qps <= 0 {
		usageAndExit("when async is set, qps is required.")
	}

	url := flag.Args()[0]
	method := strings.ToUpper(*m)
	dataType := strings.ToUpper(*dataType)

	// set content-type
	header := make(http.Header)
	header.Set("Content-Type", *contentType)
	// set any other additional headers
	if *headers != "" {
		usageAndExit("Flag '-h' is deprecated, please use '-H' instead.")
	}
	// set any other additional repeatable headers
	for _, h := range hs {
		match, err := parseInputWithRegexp(h, headerRegexp)
		if err != nil {
			usageAndExit(err.Error())
		}
		header.Set(match[1], match[2])
	}

	if *accept != "" {
		header.Set("Accept", *accept)
	}

	// set basic auth if set
	var username, password string
	if *authHeader != "" {
		match, err := parseInputWithRegexp(*authHeader, authRegexp)
		if err != nil {
			usageAndExit(err.Error())
		}
		username, password = match[1], match[2]
	}

	var requestParamSlice = new(requester.RequestParamSlice)
	var bodyAll []byte
	if *body != "" {
		bodyAll = []byte(*body)
		param := requester.RequestParam{
			Content: bodyAll,
		}
		requestParamSlice.RequestParams = append(requestParamSlice.RequestParams, param)
	}
	if *bodyFile != "" {
		slurp, err := ioutil.ReadFile(*bodyFile)
		if err != nil {
			errAndExit(err.Error())
		}
		bodyAll = slurp

		for _, row := range bytes.Split(bodyAll, []byte("\n")) {
			if !bytes.Equal(row, []byte("")) {
				param := requester.RequestParam{
					Content: row,
				}
				requestParamSlice.RequestParams = append(requestParamSlice.RequestParams, param)
			}
		}
	}

	if *output != "csv" && *output != "" {
		usageAndExit("Invalid output type; only csv is supported.")
	}

	var proxyURL *gourl.URL
	if *proxyAddr != "" {
		var err error
		proxyURL, err = gourl.Parse(*proxyAddr)
		if err != nil {
			usageAndExit(err.Error())
		}
	}

	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		usageAndExit(err.Error())
	}
	req.Header = header
	if username != "" || password != "" {
		req.SetBasicAuth(username, password)
	}

	// set host header if set
	if *hostHeader != "" {
		req.Host = *hostHeader
	}

	w := &requester.Work{
		Request: req,
		//RequestBody:        bodyAll,
		RequestParamSlice:    requestParamSlice,
		DataType:             dataType,
		N:                    num,
		C:                    conc,
		QPS:                  qps,
		SingleRequestTimeout: time.Duration(*T) * time.Second,
		PerformanceTimeout:   time.Duration(*t) * time.Second,
		DisableOutput:        *disableOutput,
		DisableCompression:   *disableCompression,
		DisableKeepAlives:    *disableKeepAlives,
		DisableRedirects:     *disableRedirects,
		RandomInput:          *randomInput,
		Async:                *async,
		H2:                   *h2,
		ProxyAddr:            proxyURL,
		Output:               *output,
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		w.Finish()
		os.Exit(1)
	}()

	w.Run()
}

func errAndExit(msg string) {
	fmt.Fprintf(os.Stderr, msg)
	fmt.Fprintf(os.Stderr, "\n")
	os.Exit(1)
}

func usageAndExit(msg string) {
	if msg != "" {
		fmt.Fprintf(os.Stderr, msg)
		fmt.Fprintf(os.Stderr, "\n\n")
	}
	flag.Usage()
	fmt.Fprintf(os.Stderr, "\n")
	os.Exit(1)
}

func parseInputWithRegexp(input, regx string) ([]string, error) {
	re := regexp.MustCompile(regx)
	matches := re.FindStringSubmatch(input)
	if len(matches) < 1 {
		return nil, fmt.Errorf("could not parse the provided input; input = %v", input)
	}
	return matches, nil
}

type headerSlice []string

func (h *headerSlice) String() string {
	return fmt.Sprintf("%s", *h)
}

func (h *headerSlice) Set(value string) error {
	*h = append(*h, value)
	return nil
}
