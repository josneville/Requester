package util

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"

	"github.com/Sirupsen/logrus"
)

type Requester interface {
	Send() (int, error)
}

type RequesterBuilder interface {
	Method(string) RequesterBuilder
	URL(string) RequesterBuilder
	Headers(map[string][]string) RequesterBuilder
	Response(interface{}) RequesterBuilder
	TransactionID(string) RequesterBuilder
	BuildMultipart(*http.Request, map[string]string, map[string]string) Requester
	BuildJSON(interface{}) Requester
	BuildOctet(*http.Request, string) Requester
}

type requesterBuilder struct {
	method   string
	url      string
	headers  map[string][]string
	tid      string
	response interface{}
}

func (rb *requesterBuilder) Method(method string) RequesterBuilder {
	rb.method = method
	return rb
}

func (rb *requesterBuilder) URL(url string) RequesterBuilder {
	rb.url = url
	return rb
}

func (rb *requesterBuilder) Headers(headers map[string][]string) RequesterBuilder {
	if rb.headers == nil {
		rb.headers = make(map[string][]string)
	}
	rb.headers = headers
	return rb
}

func (rb *requesterBuilder) Response(response interface{}) RequesterBuilder {
	rb.response = response
	return rb
}

func (rb *requesterBuilder) TransactionID(tid string) RequesterBuilder {
	rb.tid = tid
	return rb
}

// BuildOctet will send a file in application/octet-stream format independent
// of original mimetype to a given url.
func (rb *requesterBuilder) BuildOctet(request *http.Request, fileName string) Requester {
	body := &bytes.Buffer{}
	file, _, err := request.FormFile(fileName)
	if err != nil {
		return &requester{err: err}
	}
	defer file.Close()
	_, err = io.Copy(body, file)
	if err != nil {
		return &requester{err: err}
	}
	req, err := http.NewRequest(rb.method, rb.url, body)
	if err != nil {
		return &requester{err: err}
	}
	req.Header.Set(models.TransactionIDKey, rb.tid)
	if rb.headers != nil {
		for k, h := range rb.headers {
			for _, v := range h {
				req.Header.Add(k, v)
			}
		}
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if rb.tid != "" {
		req.Header.Set("X-Transaction-Id", rb.tid)
	}
	return &requester{
		req:  *req,
		resp: rb.response,
		tid:  rb.tid,
	}
}

func (rb *requesterBuilder) BuildJSON(j interface{}) Requester {
	var reqBody *bytes.Reader
	reqBody = nil
	if j != nil {
		reqBytes, err := json.Marshal(j)
		if err != nil {
			return &requester{err: err}
		}
		reqBody = bytes.NewReader(reqBytes)
	}

	var req *http.Request
	// Create the request given the parameters
	var err error
	if reqBody == nil {
		req, err = http.NewRequest(rb.method, rb.url, nil)
	} else {
		req, err = http.NewRequest(rb.method, rb.url, reqBody)
	}

	if err != nil {
		return &requester{err: err}
	}
	req.Header.Set(models.TransactionIDKey, rb.tid)
	if rb.headers != nil {
		for k, h := range rb.headers {
			for _, v := range h {
				req.Header.Add(k, v)
			}
		}
	}
	req.Header.Set("Content-Type", "application/json")
	if rb.tid != "" {
		req.Header.Set("X-Transaction-Id", rb.tid)
	}
	return &requester{
		req:  *req,
		resp: rb.response,
		tid:  rb.tid,
	}
}

func (rb *requesterBuilder) BuildMultipart(request *http.Request, files map[string]string, params map[string]string) Requester {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for newName, currentName := range files {
		file, _, err := request.FormFile(currentName)
		if err != nil {
			return &requester{err: err}
		}
		defer file.Close()
		part, err := writer.CreateFormFile(newName, currentName)
		if err != nil {
			return &requester{err: err}
		}
		_, err = io.Copy(part, file)
		if err != nil {
			return &requester{err: err}
		}
	}

	for key, val := range params {
		_ = writer.WriteField(key, val)
	}

	err := writer.Close()
	if err != nil {
		return &requester{err: err}
	}
	req, err := http.NewRequest(rb.method, rb.url, body)
	if err != nil {
		return &requester{err: err}
	}
	req.Header.Set(models.TransactionIDKey, rb.tid)
	if rb.headers != nil {
		for k, h := range rb.headers {
			for _, v := range h {
				req.Header.Add(k, v)
			}
		}
	}
	return &requester{
		req:  *req,
		resp: rb.response,
		tid:  rb.tid,
	}
}

func NewRequester() RequesterBuilder {
	return &requesterBuilder{}
}

type requester struct {
	req  http.Request
	resp interface{}
	tid  string
	err  error
}

func (r *requester) Send() (int, error) {
	if r.err != nil {
		return http.StatusInternalServerError, fmt.Errorf(`Unknown error: %s`, r.err.Error())
	}
	client := &http.Client{}

	resp, err := client.Do(&r.req)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf(`Error encountered when
      making request: %s`, err.Error())
	}
	defer resp.Body.Close()

	if r.resp != nil {
		// Read the body into a buffer so we can print it in case of a parse error.
		buf, _ := ioutil.ReadAll(resp.Body)

		if resp.Header.Get("Content-Encoding") == "gzip" {
			gr, err := gzip.NewReader(bytes.NewReader(buf))
			if err != nil {
				return http.StatusInternalServerError, fmt.Errorf(`Unable to create gzip reader for encoded content: %s`, err.Error())
			}
			defer gr.Close()
			unzipped, err := ioutil.ReadAll(gr)
			buf = unzipped
		}

		decoder := json.NewDecoder(bytes.NewReader(buf))
		err := decoder.Decode(&r.resp)
		if err != nil {
			LogIt(Error, fmt.Sprintf("Service call returned non-json response body."),
				logrus.Fields{
					"type":        "internal",
					"transaction": r.tid,
					"err": map[string]interface{}{
						"message": err.Error(),
					},
					"json": string(buf),
				})

			return http.StatusInternalServerError, fmt.Errorf(`Unable to unmarshal
        response object to provided model: %s`, err.Error())
		}
	}

	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("Non-200 status code returned from service call")
	}

	return resp.StatusCode, nil
}
