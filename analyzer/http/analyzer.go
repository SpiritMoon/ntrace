package http

/*
#cgo CFLAGS: -I./http_parser/
#cgo LDFLAGS: -L${SRCDIR}/http_parser -lhttp_parser

#include <http_parser.h>

extern int onReqMessageBegin_cgo(http_parser* parser);
extern int onReqURL_cgo(http_parser* parser, const char *from, size_t length);
extern int onReqHeaderField_cgo(http_parser* parser, const char *from, size_t length);
extern int onReqHeaderValue_cgo(http_parser* parser, const char *from, size_t length);
extern int onReqHeadersComplete_cgo(http_parser* parser);
extern int onReqBody_cgo(http_parser* parser, const char *from, size_t length);
extern int onReqMessageComplete_cgo(http_parser* parser);

extern int onRespMessageBegin_cgo(http_parser* parser);
extern int onRespURL_cgo(http_parser* parser, const char *from, size_t length);
extern int onRespHeaderField_cgo(http_parser* parser, const char *from, size_t length);
extern int onRespHeaderValue_cgo(http_parser* parser, const char *from, size_t length);
extern int onRespHeadersComplete_cgo(http_parser* parser);
extern int onRespBody_cgo(http_parser* parser, const char *from, size_t length);
extern int onRespMessageComplete_cgo(http_parser* parser);
*/
import "C"

import (
	"container/list"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"time"
	"unsafe"
)

const (
	ProtoName = "HTTP"
)

type sessionState uint16

const (
	sessionInit sessionState = iota
	requestHeaderBegin
	requestHeaderComplete
	requestBodyBegin
	requestBodyComplete
	responseHeaderBegin
	responseHeaderComplete
	responseBodyBegin
	responseBodyComplete
)

func (s sessionState) String() string {
	switch s {
	case sessionInit:
		return "HttpSessionInit"

	case requestHeaderBegin:
		return "HttpRequestHeaderBegin"

	case requestHeaderComplete:
		return "HttpRequestHeaderComplete"

	case requestBodyBegin:
		return "HttpRequestBodyBegin"

	case requestBodyComplete:
		return "HttpRequestBodyComplete"

	case responseHeaderBegin:
		return "HttpResponseHeaderBegin"

	case responseHeaderComplete:
		return "HttpResponseHeaderComplete"

	case responseBodyBegin:
		return "HttpResponseBodyBegin"

	case responseBodyComplete:
		return "HttpResponseBodyComplete"

	default:
		return "InvalidHttpSessionState"
	}
}

type header struct {
	name  string
	value string
}

type session struct {
	resetFlag      bool
	state          sessionState
	reqVer         string
	reqMethod      string
	reqURI         string
	reqHeaders     []header
	reqHeaderSize  uint
	reqBodySize    uint
	respVer        string
	respHeaders    []header
	statusCode     uint16
	respHeaderSize uint
	respBodySize   uint

	reqTime          time.Time
	respBeginTime    time.Time
	respCompleteTime time.Time
}

func (s session) session2Breakdown() *SessionBreakdown {
	sb := new(SessionBreakdown)

	if s.resetFlag {
		sb.SessionState = "Reset:" + s.state.String()
	} else {
		sb.SessionState = s.state.String()
	}

	sb.ReqVer = s.reqVer
	sb.ReqMethod = s.reqMethod
	sb.ReqURI = s.reqURI
	sb.ReqHeaders = make(map[string]string)
	for _, h := range s.reqHeaders {
		sb.ReqHeaders[h.name] = h.value
	}
	sb.ReqHeaderSize = s.reqHeaderSize
	sb.ReqBodySize = s.reqBodySize

	sb.RespVer = s.respVer
	sb.RespHeaders = make(map[string]string)
	for _, h := range s.respHeaders {
		sb.RespHeaders[h.name] = h.value
	}
	sb.StatusCode = s.statusCode
	sb.RespHeaderSize = s.respHeaderSize
	sb.RespBodySize = s.respBodySize

	if s.respBeginTime.After(s.reqTime) {
		sb.ServerLatency = uint(s.respBeginTime.Sub(s.reqTime).Nanoseconds() / 1000000)
	}
	if s.respCompleteTime.After(s.respBeginTime) {
		sb.DownloadLatency = uint(s.respCompleteTime.Sub(s.respBeginTime).Nanoseconds() / 1000000)
	}

	return sb
}

type SessionBreakdown struct {
	SessionState    string            `json:"session_state"`
	ReqVer          string            `json:"request_version"`
	ReqMethod       string            `json:"request_method"`
	ReqURI          string            `json:"request_uri"`
	ReqHeaders      map[string]string `json:"request_headers"`
	ReqHeaderSize   uint              `json:"request_header_size"`
	ReqBodySize     uint              `json:"request_body_size"`
	RespVer         string            `json:"response_version"`
	RespHeaders     map[string]string `json:"response_headers"`
	StatusCode      uint16            `json:"response_status_code"`
	RespHeaderSize  uint              `json:"response_header_size"`
	RespBodySize    uint              `json:"response_body_size"`
	ServerLatency   uint              `json:"server_latency"`
	DownloadLatency uint              `json:"download_latency"`
}

//export onReqMessageBegin
func onReqMessageBegin(parser *C.http_parser) C.int {
	analyzer := (*Analyzer)(unsafe.Pointer(uintptr(parser.customData)))

	currSession := new(session)
	currSession.state = requestHeaderBegin
	currSession.reqTime = analyzer.timestamp
	analyzer.sessions.PushBack(currSession)

	return C.int(0)
}

//export onReqURL
func onReqURL(parser *C.http_parser, from *C.char, length C.size_t) C.int {
	analyzer := (*Analyzer)(unsafe.Pointer(uintptr(parser.customData)))

	if back := analyzer.sessions.Back(); back != nil {
		currSession := back.Value.(*session)

		currSession.reqMethod = C.GoString((*C.char)(C.http_method_str(C.enum_http_method(parser.method))))
		currSession.reqURI = C.GoStringN(from, C.int(length))
	} else {
		log.Error("http.Analyzer:onReqURL does not find session.")
	}

	return C.int(0)
}

//export onReqHeaderField
func onReqHeaderField(parser *C.http_parser, from *C.char, length C.size_t) C.int {
	analyzer := (*Analyzer)(unsafe.Pointer(uintptr(parser.customData)))

	if back := analyzer.sessions.Back(); back != nil {
		currSession := back.Value.(*session)
		currSession.state = requestHeaderBegin
		headerName := C.GoStringN(from, C.int(length))
		currSession.reqHeaders = append(currSession.reqHeaders, header{name: headerName})
	} else {
		log.Error("http.Analyzer:onReqHeaderField does not find session.")
	}

	return C.int(0)
}

//export onReqHeaderValue
func onReqHeaderValue(parser *C.http_parser, from *C.char, length C.size_t) C.int {
	analyzer := (*Analyzer)(unsafe.Pointer(uintptr(parser.customData)))

	if back := analyzer.sessions.Back(); back != nil {
		currSession := back.Value.(*session)

		headerValue := C.GoStringN(from, C.int(length))
		currSession.reqHeaders[len(currSession.reqHeaders)-1].value = headerValue
	} else {
		log.Error("http.Analyzer:onReqHeaderValue does not find session.")
	}

	return C.int(0)
}

//export onReqHeadersComplete
func onReqHeadersComplete(parser *C.http_parser) C.int {
	analyzer := (*Analyzer)(unsafe.Pointer(uintptr(parser.customData)))

	if back := analyzer.sessions.Back(); back != nil {
		currSession := back.Value.(*session)
		currSession.state = requestHeaderComplete

		currSession.reqVer = fmt.Sprintf("HTTP/%d.%d", parser.http_major, parser.http_minor)
		currSession.reqHeaderSize = uint(parser.nread)
	} else {
		log.Error("http.Analyzer:onReqURL does not find session.")
	}

	return C.int(0)
}

//export onReqBody
func onReqBody(parser *C.http_parser, from *C.char, length C.size_t) C.int {
	analyzer := (*Analyzer)(unsafe.Pointer(uintptr(parser.customData)))

	if back := analyzer.sessions.Back(); back != nil {
		currSession := back.Value.(*session)
		currSession.state = requestBodyBegin

		currSession.reqBodySize += uint(length)
	} else {
		log.Error("http.Analyzer:onReqURL does not find session.")
	}

	return C.int(0)
}

//export onReqMessageComplete
func onReqMessageComplete(parser *C.http_parser) C.int {
	analyzer := (*Analyzer)(unsafe.Pointer(uintptr(parser.customData)))

	if back := analyzer.sessions.Back(); back != nil {
		currSession := back.Value.(*session)
		currSession.state = requestBodyComplete
	} else {
		log.Error("http.Analyzer:onReqURL does not find session.")
	}

	return C.int(0)
}

//export onRespMessageBegin
func onRespMessageBegin(parser *C.http_parser) C.int {
	analyzer := (*Analyzer)(unsafe.Pointer(uintptr(parser.customData)))

	if front := analyzer.sessions.Front(); front != nil {
		currSession := front.Value.(*session)

		currSession.respBeginTime = analyzer.timestamp
	} else {
		log.Error("http.Analyzer:onReqURL does not find session.")
	}

	return C.int(0)
}

//export onRespURL
func onRespURL(parser *C.http_parser, from *C.char, length C.size_t) C.int {
	return C.int(0)
}

//export onRespHeaderField
func onRespHeaderField(parser *C.http_parser, from *C.char, length C.size_t) C.int {
	analyzer := (*Analyzer)(unsafe.Pointer(uintptr(parser.customData)))

	if front := analyzer.sessions.Front(); front != nil {
		currSession := front.Value.(*session)
		currSession.state = responseHeaderBegin

		headerName := C.GoStringN(from, C.int(length))
		currSession.respHeaders = append(currSession.respHeaders, header{name: headerName})
	} else {
		log.Error("http.Analyzer:onReqURL does not find session.")
	}

	return C.int(0)
}

//export onRespHeaderValue
func onRespHeaderValue(parser *C.http_parser, from *C.char, length C.size_t) C.int {
	analyzer := (*Analyzer)(unsafe.Pointer(uintptr(parser.customData)))

	if front := analyzer.sessions.Front(); front != nil {
		currSession := front.Value.(*session)
		currSession.state = responseHeaderBegin

		headerValue := C.GoStringN(from, C.int(length))
		currSession.respHeaders[len(currSession.respHeaders)-1].value = headerValue
	} else {
		log.Error("http.Analyzer:onReqURL does not find session.")
	}

	return C.int(0)
}

//export onRespHeadersComplete
func onRespHeadersComplete(parser *C.http_parser) C.int {
	analyzer := (*Analyzer)(unsafe.Pointer(uintptr(parser.customData)))

	if front := analyzer.sessions.Front(); front != nil {
		currSession := front.Value.(*session)
		currSession.state = responseHeaderComplete

		currSession.statusCode = uint16(parser.status_code)
		currSession.respVer = fmt.Sprintf("HTTP/%d.%d", parser.http_major, parser.http_minor)
		currSession.respHeaderSize = uint(parser.nread)
	} else {
		log.Error("http.Analyzer:onReqURL does not find session.")
	}

	return C.int(0)
}

//export onRespBody
func onRespBody(parser *C.http_parser, from *C.char, length C.size_t) C.int {
	analyzer := (*Analyzer)(unsafe.Pointer(uintptr(parser.customData)))

	if front := analyzer.sessions.Front(); front != nil {
		currSession := front.Value.(*session)
		currSession.state = responseBodyBegin

		currSession.respBodySize += uint(length)
	} else {
		log.Error("http.Analyzer:onReqURL does not find session.")
	}

	return C.int(0)
}

//export onRespMessageComplete
func onRespMessageComplete(parser *C.http_parser) C.int {
	analyzer := (*Analyzer)(unsafe.Pointer(uintptr(parser.customData)))

	if front := analyzer.sessions.Front(); front != nil {
		currSession := front.Value.(*session)
		currSession.state = responseBodyComplete

		currSession.respCompleteTime = analyzer.timestamp
	} else {
		log.Error("http.Analyzer:onReqURL does not find session.")
	}

	return C.int(0)
}

type Analyzer struct {
	timestamp          time.Time
	reqParser          C.http_parser
	reqParserSettings  C.http_parser_settings
	respParser         C.http_parser
	respParserSettings C.http_parser_settings
	sessions           list.List
}

func (a *Analyzer) Init() {
	C.http_parser_init(&a.reqParser, C.HTTP_REQUEST)
	a.reqParser.customData = C.uint64_t(uintptr(unsafe.Pointer(a)))
	a.reqParserSettings.on_message_begin = C.http_cb(unsafe.Pointer(C.onReqMessageBegin_cgo))
	a.reqParserSettings.on_url = C.http_data_cb(unsafe.Pointer(C.onReqURL_cgo))
	a.reqParserSettings.on_header_field = C.http_data_cb(unsafe.Pointer(C.onReqHeaderField_cgo))
	a.reqParserSettings.on_header_value = C.http_data_cb(unsafe.Pointer(C.onReqHeaderValue_cgo))
	a.reqParserSettings.on_headers_complete = C.http_cb(unsafe.Pointer(C.onReqHeadersComplete_cgo))
	a.reqParserSettings.on_body = C.http_data_cb(unsafe.Pointer(C.onReqBody_cgo))
	a.reqParserSettings.on_message_complete = C.http_cb(unsafe.Pointer(C.onReqMessageComplete_cgo))

	C.http_parser_init(&a.respParser, C.HTTP_RESPONSE)
	a.respParser.customData = C.uint64_t(uintptr(unsafe.Pointer(a)))
	a.respParserSettings.on_message_begin = C.http_cb(unsafe.Pointer(C.onRespMessageBegin_cgo))
	a.respParserSettings.on_url = C.http_data_cb(unsafe.Pointer(C.onRespURL_cgo))
	a.respParserSettings.on_header_field = C.http_data_cb(unsafe.Pointer(C.onRespHeaderField_cgo))
	a.respParserSettings.on_header_value = C.http_data_cb(unsafe.Pointer(C.onRespHeaderValue_cgo))
	a.respParserSettings.on_headers_complete = C.http_cb(unsafe.Pointer(C.onRespHeadersComplete_cgo))
	a.respParserSettings.on_body = C.http_data_cb(unsafe.Pointer(C.onRespBody_cgo))
	a.respParserSettings.on_message_complete = C.http_cb(unsafe.Pointer(C.onRespMessageComplete_cgo))

	a.sessions.Init()
}

func (a *Analyzer) Proto() (protoName string) {
	return ProtoName
}

func (a *Analyzer) HandleEstb(timestamp time.Time) {
	log.Debug("Http Analyzer: HandleEstb.")
}

func (a *Analyzer) HandleData(payload []byte, fromClient bool, timestamp time.Time) (parseBytes int, sessionBreakdown interface{}) {
	a.timestamp = timestamp

	var parsed C.size_t
	var sessionElement *list.Element
	var currSession *session
	if fromClient {
		parsed = C.http_parser_execute(&a.reqParser, &a.reqParserSettings, (*C.char)(unsafe.Pointer(&payload[0])), C.size_t(len(payload)))
		if sessionElement = a.sessions.Back(); sessionElement != nil {
			currSession = sessionElement.Value.(*session)
		} else {
			currSession = nil
		}
	} else {
		parsed = C.http_parser_execute(&a.respParser, &a.respParserSettings, (*C.char)(unsafe.Pointer(&payload[0])), C.size_t(len(payload)))
		if sessionElement = a.sessions.Front(); sessionElement != nil {
			currSession = sessionElement.Value.(*session)
		} else {
			currSession = nil
		}
	}

	if currSession == nil || currSession.state != responseBodyComplete {
		return int(parsed), nil
	}

	a.sessions.Remove(sessionElement)
	return int(parsed), currSession.session2Breakdown()
}

func (a *Analyzer) HandleReset(fromClient bool, timestamp time.Time) (sessionBreakdown interface{}) {
	if fromClient {
		log.Debug("Http Analyzer: HandleReset from client.")
	} else {
		log.Debug("Http Analyzer: HandleReset from server.")
	}

	if front := a.sessions.Front(); front != nil {
		currSession := *front.Value.(*session)
		if !fromClient && currSession.state == responseBodyBegin {
			currSession.state = responseBodyComplete
			currSession.respCompleteTime = timestamp
		} else {
			currSession.resetFlag = true
		}
		a.sessions.Remove(front)

		return currSession.session2Breakdown()
	}

	return nil
}

func (a *Analyzer) HandleFin(fromClient bool, timestamp time.Time) (sessionBreakdown interface{}) {
	if fromClient {
		log.Debug("Http Analyzer: HandleFin from client.")
	} else {
		log.Debug("Http Analyzer: HandleFin from server.")
	}

	if front := a.sessions.Front(); front != nil && !fromClient {
		if currSession := front.Value.(*session); currSession.state == responseBodyBegin {
			currSession.state = responseBodyComplete
			currSession.respCompleteTime = timestamp
			a.sessions.Remove(front)

			return currSession.session2Breakdown()
		}
	}

	return nil
}

func DetectProto(payload []byte, fromClient bool, timestamp time.Time) (proto string) {
	preLen := 0
	payloadLen := len(payload)

	if fromClient {
		switch {
		// Normal
		case payloadLen >= 7 && string(payload[:7]) == "DELETE ":
			preLen = 7

		case payloadLen >= 4 && string(payload[:4]) == "GET ":
			preLen = 4

		case payloadLen >= 5 && string(payload[:5]) == "HEAD ":
			preLen = 5

		case payloadLen >= 4 && string(payload[:4]) == "PUT ":
			preLen = 4

			// Pathological
		case payloadLen >= 8 && string(payload[:8]) == "CONNECT ":
			preLen = 8

		case payloadLen >= 8 && string(payload[:8]) == "OPTIONS ":
			preLen = 8

		case payloadLen >= 6 && string(payload[:6]) == "TRACE ":
			preLen = 6

			// WebDAV
		case payloadLen >= 5 && string(payload[:5]) == "COPY ":
			preLen = 5

		case payloadLen >= 5 && string(payload[:5]) == "LOCK ":
			preLen = 5

		case payloadLen >= 6 && string(payload[:6]) == "MKCOL ":
			preLen = 6

		case payloadLen >= 5 && string(payload[:5]) == "MOVE ":
			preLen = 5

		case payloadLen >= 9 && string(payload[:9]) == "PROPFIND ":
			preLen = 9

		case payloadLen >= 10 && string(payload[:10]) == "PROPPATCH ":
			preLen = 10

		case payloadLen >= 7 && string(payload[:7]) == "SEARCH ":
			preLen = 7

		case payloadLen >= 7 && string(payload[:7]) == "UNLOCK ":
			preLen = 7

			// Subversion
		case payloadLen >= 7 && string(payload[:7]) == "REPORT ":
			preLen = 7

		case payloadLen >= 11 && string(payload[:11]) == "MKACTIVITY ":
			preLen = 11

		case payloadLen >= 9 && string(payload[:9]) == "CHECKOUT ":
			preLen = 9

		case payloadLen >= 6 && string(payload[:6]) == "MERGE ":
			preLen = 6

			// Upnp
		case payloadLen >= 8 && string(payload[:8]) == "MSEARCH ":
			preLen = 8

		case payloadLen >= 7 && string(payload[:7]) == "NOTIFY ":
			preLen = 7

		case payloadLen >= 10 && string(payload[:10]) == "SUBSCRIBE ":
			preLen = 10

		case payloadLen >= 12 && string(payload[:12]) == "UNSUBSCRIBE ":
			preLen = 12

			// RFC-5789
		case payloadLen >= 6 && string(payload[:6]) == "PATCH ":
			preLen = 7

		case payloadLen >= 6 && string(payload[:6]) == "PURGE ":
			preLen = 7

		default:
			preLen = 0
		}

		if preLen > 0 {
			for i := preLen; i < payloadLen-1; i++ {
				if string(payload[i]) == "\r" &&
					string(payload[i+1]) == "\n" &&
					(string(payload[i-8]) == "HTTP/1.0" || string(payload[i-8]) == "HTTP/1.1") {
					return ProtoName
				}
			}
		}
	} else {
		if payloadLen >= 8 &&
			(string(payload[:8]) == "HTTP/1.0" || string(payload[:8]) == "HTTP/1.1") {
			return ProtoName
		}
	}

	return ""
}
