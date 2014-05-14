package http2

import (
	. "github.com/jxck/http2/frame"
)

const (
	OVER_TLS           string = "h2"
	OVER_TCP                  = "h2c"
	VERSION                   = "-12"
	CONNECTION_PREFACE        = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"
)

var DefaultSettings = map[SettingsId]uint32{
	SETTINGS_MAX_CONCURRENT_STREAMS: 100,
	SETTINGS_INITIAL_WINDOW_SIZE:    DEFAULT_WINDOW_SIZE,
}

var NilSettings = make(map[SettingsId]uint32, 0)
