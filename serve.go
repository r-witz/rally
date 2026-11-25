package rally

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
)

type operationHandler[I, O any] struct {
	*plan
	handler func(context.Context, *I) (*O, error)
}

func (h *operationHandler[I, O]) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer recoverPanic(w, r)
	if h.hasOut && !acceptsJSON(r.Header["Accept"]) {
		writeErrorBody(w, NewError(http.StatusNotAcceptable, "the response is application/json, which the Accept header excludes"))
		return
	}
	s := getState()
	defer putState(s)
	in := new(I)
	if (len(h.params) > 0 || h.hasBody || r.URL.RawQuery != "") && !h.readInput(w, r, reflect.ValueOf(in).Elem(), &s.dec) {
		return
	}
	out, err := h.handler(r.Context(), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if out == nil {
		if h.hasOut {
			writeError(w, r, errors.New("the handler returned a nil output without an error"))
			return
		}
		w.WriteHeader(h.defaultStatus)
		return
	}
	h.writeOutput(w, r, reflect.ValueOf(out).Elem(), &s.enc)
}

const maxQueryParams = 10000

var errBodyTooLarge = errors.New("request body too large")

var jsonContentType = []string{"application/json"}

const netHTTPSetsContentLengthUpTo = 2048

type state struct {
	dec decodeState
	enc encodeState
}

const maxPooledBytes = 1 << 20

var statePool = sync.Pool{New: func() any { return &state{enc: encodeState{buf: make([]byte, 0, 1024)}} }}

func getState() *state { return statePool.Get().(*state) }

func putState(s *state) {
	d, e := &s.dec, &s.enc
	if cap(d.body)+cap(d.scratch)+cap(e.buf) > maxPooledBytes {
		return
	}
	clear(d.errs)
	clear(d.query)
	*d = decodeState{errs: d.errs[:0], frames: d.frames[:0], scratch: d.scratch, body: d.body, query: d.query[:0]}
	*e = encodeState{buf: e.buf[:0]}
	statePool.Put(s)
}

func (p *plan) readInput(w http.ResponseWriter, r *http.Request, in reflect.Value, d *decodeState) bool {
	if q := r.URL.RawQuery; q != "" && strings.Count(q, "&") >= maxQueryParams {
		writeErrorBody(w, Error400BadRequest("too many query parameters"))
		return false
	}
	if len(p.params) > 0 || r.URL.RawQuery != "" {
		p.bindParams(d, r, in)
	}
	status, msg := 0, ""
	if p.hasBody {
		status, msg = p.readBody(d, r, in)
	}
	if status == 0 {
		if len(d.errs) == 0 {
			return true
		}
		status, msg = http.StatusUnprocessableEntity, "validation failed"
		if d.errs[0].In != "body" {
			status = http.StatusBadRequest
		}
	}
	m := NewError(status, msg)
	m.Errors = d.errs
	writeErrorBody(w, m)
	return false
}

func (p *plan) readBody(d *decodeState, r *http.Request, in reflect.Value) (int, string) {
	ct := r.Header["Content-Type"]
	if len(ct) > 1 || len(ct) == 1 && !isJSONMediaType(ct[0]) {
		return http.StatusUnsupportedMediaType, "unsupported content type: send application/json"
	}
	if r.ContentLength > p.maxBody {
		return http.StatusRequestEntityTooLarge, fmt.Sprintf("request body is too large limit=%d bytes", p.maxBody)
	}
	announced := int(min(max(r.ContentLength+1, 4096), 64<<10))
	buf, err := readAll(r.Body, slices.Grow(d.body[:0], announced), p.maxBody)
	d.body = buf
	if errors.Is(err, errBodyTooLarge) {
		return http.StatusRequestEntityTooLarge, fmt.Sprintf("request body is too large limit=%d bytes", p.maxBody)
	}
	if err != nil {
		return http.StatusBadRequest, "cannot read request body"
	}
	if len(buf) == 0 {
		if p.bodyRequired {
			return http.StatusBadRequest, "request body is required"
		}
		return 0, ""
	}
	if len(ct) == 0 {
		return http.StatusUnsupportedMediaType, "missing content type: send application/json"
	}
	if err := d.decode(p.bodyDec, buf, in.Field(p.bodyIndex), "body"); err != nil {
		d.errs = append(d.errs, ErrorDetail{Detail: err.Error(), Code: "syntax", In: "body"})
		return http.StatusBadRequest, "malformed JSON"
	}
	return 0, ""
}

func readAll(r io.Reader, buf []byte, limit int64) ([]byte, error) {
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, err := r.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if int64(len(buf)) > limit {
			return buf, errBodyTooLarge
		}
		if err == io.EOF {
			return buf, nil
		}
		if err != nil {
			return buf, err
		}
	}
}

func acceptsJSON(fields []string) bool {
	if len(fields) == 0 {
		return true
	}
	best, accepted := -1, false
	for _, field := range fields {
		for r := range strings.SplitSeq(field, ",") {
			mt, params, _ := strings.Cut(r, ";")
			mt = strings.TrimSpace(mt)
			spec := -1
			switch {
			case strings.EqualFold(mt, "application/json"):
				spec = 2
			case strings.EqualFold(mt, "application/*"):
				spec = 1
			case mt == "*/*":
				spec = 0
			}
			if spec < 0 || spec < best {
				continue
			}
			ok := true
			for p := range strings.SplitSeq(params, ";") {
				if k, v, _ := strings.Cut(p, "="); strings.EqualFold(strings.TrimSpace(k), "q") {
					v = strings.TrimSpace(v)
					ok = strings.Trim(v, "0.") != "" || v == ""
				}
			}
			if spec > best || ok {
				best, accepted = spec, ok
			}
		}
	}
	return accepted
}

func isJSONMediaType(ct string) bool {
	if ct == "application/json" {
		return true
	}
	mt, _, _ := strings.Cut(ct, ";")
	mt = strings.TrimSpace(mt)
	if strings.EqualFold(mt, "application/json") {
		return true
	}
	slash := strings.IndexByte(mt, '/')
	return slash > 0 && len(mt) > slash+len("+json") && strings.EqualFold(mt[len(mt)-len("+json"):], "+json")
}

func (p *plan) writeOutput(w http.ResponseWriter, r *http.Request, out reflect.Value, e *encodeState) {
	status := p.defaultStatus
	if p.hasStatus {
		if s := int(out.Field(p.statusIndex).Int()); s != 0 && s != status {
			if !slices.Contains(p.statuses, s) {
				writeError(w, r, fmt.Errorf("status %d is not in Operation.Statuses", s))
				return
			}
			status = s
		}
	}
	hasBody := p.hasOut && !bodiless(status)
	if hasBody {
		if err := e.encode(p.outEnc, out.Field(p.outIndex)); err != nil {
			writeError(w, r, fmt.Errorf("encoding response body: %w", err))
			return
		}
	}
	h := w.Header()
	for i := range p.headers {
		hw := &p.headers[i]
		if v := fieldByIndex(out, hw.index).String(); v != "" {
			h[hw.key] = []string{v}
		}
	}
	if !hasBody {
		w.WriteHeader(status)
		return
	}
	if _, ok := h["Content-Type"]; !ok {
		h["Content-Type"] = jsonContentType
	}
	if len(e.buf) > netHTTPSetsContentLengthUpTo {
		h["Content-Length"] = []string{strconv.Itoa(len(e.buf))}
	}
	w.WriteHeader(status)
	_, _ = w.Write(e.buf)
}
