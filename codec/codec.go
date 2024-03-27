package codec

import "io"

type Header struct {
	ServiceMethod string // format "Service.Method"
	Seq           uint64 // Seq code from client
	Error         string
}

// default codec func
type Codec interface {
	io.Closer                         // close io connect
	ReadHeader(*Header) error         // get msg form header
	ReadBody(interface{}) error       // get msg form body
	Write(*Header, interface{}) error // write msg to header and body
}

// NewCodecFunc init codec func
type NewCodecFunc func(io.ReadWriteCloser) Codec

type Type string

const (
	GobType  Type = "application/gob"
	JsonType Type = "application/json"
)

// NewCodecFuncMap store all Codec func
var NewCodecFuncMap map[Type]NewCodecFunc

func init() {
	NewCodecFuncMap = make(map[Type]NewCodecFunc)
	NewCodecFuncMap[GobType] = NewGobCodec
	NewCodecFuncMap[JsonType] = NewJsonCodec
}
