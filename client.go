package simplerpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/ChenMiaoQiu/simple-rpc/codec"
)

// Call represents an use of RPC.
type Call struct {
	Seq           uint64
	ServiceMethod string      // format "<service>.<method>"
	Args          interface{} // arguments to the function
	Reply         interface{} // reply from the function
	Error         error       // if error occurs, it will be set
	Done          chan *Call  // Strobes when call is complete.
}

// done is used when rpc complete serve
func (call *Call) done() {
	call.Done <- call
}

// Client represents an RPC Client.
// There may be multiple outstanding Calls associated
// with a single Client, and a Client may be used by
// multiple goroutines simultaneously.
type Client struct {
	cc       codec.Codec
	opt      *Option
	sending  sync.Mutex // protect following
	mu       sync.Mutex // protect following
	header   codec.Header
	seq      uint64
	pending  map[uint64]*Call // store unserved calls
	closing  bool             // user has called Close
	shutdown bool             // server has told us to stop
}

var _ io.Closer = (*Client)(nil)

type clientResult struct {
	client *Client
	err    error
}

// create new client
type newClientFunc func(conn net.Conn, opt *Option) (client *Client, err error)

// ErrShutdown default errors when connect shut down
var ErrShutdown = errors.New("connection is shut down")

// Close close the connection
func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()

	if client.closing {
		return ErrShutdown
	}

	client.closing = true
	return client.cc.Close()
}

// IsAvailable return true if the client does work
func (client *Client) IsAvailable() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	// if client does work the client not closed and the
	// server not shutdown
	return !client.shutdown && !client.closing
}

// registerCall add a call to pending, that wating to send
func (client *Client) registerCall(call *Call) (uint64, error) {
	client.mu.Lock()
	defer client.mu.Unlock()

	// check if the client actived because mu Lock,
	// so don't use IsAvailable
	if client.closing || client.shutdown {
		return 0, ErrShutdown
	}

	// register call to pending
	call.Seq = client.seq
	client.pending[call.Seq] = call
	client.seq++
	return call.Seq, nil
}

// removeCall remove call form pending
func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	call := client.pending[seq]
	delete(client.pending, seq)
	return call
}

// terminateCalls send err msg to panding's call and
// close the client when get error from server or client
func (client *Client) terminateCalls(err error) {
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

// receive receive call msg
func (client *Client) receive() {
	var err error

	for err == nil {
		var h codec.Header
		if err = client.cc.ReadHeader(&h); err != nil {
			break
		}
		call := client.removeCall(h.Seq)
		switch {
		case call == nil:
			// it usually means that Write partially failed
			// and call was already removed.
			err = client.cc.ReadBody(nil)
		case h.Error != "":
			// server serve err
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil)
			call.done()
		default:
			// success served, read msg from body
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body " + err.Error())
			}
			call.done()
		}
	}

	// error occurs, so terminateCalls pending calls
	client.terminateCalls(err)
}

// NewClient get new client
func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	// get codec that matched
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		log.Println("rpc client: codec error:", err)
		return nil, err
	}
	// send options with server
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client: options error: ", err)
		_ = conn.Close()
		return nil, err
	}
	return newClientCodec(f(conn), opt), nil
}

// newClientCodec create new client and receive serve msg
func newClientCodec(cc codec.Codec, opt *Option) *Client {
	client := &Client{
		seq:     1, // seq starts with 1, 0 means invalid call
		cc:      cc,
		opt:     opt,
		pending: make(map[uint64]*Call),
	}
	go client.receive()
	return client
}

// parseOptions get user set option
func parseOptions(opts ...*Option) (*Option, error) {
	// if opts is nil or pass nil as parameter
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	}
	if len(opts) != 1 {
		return nil, errors.New("number of options is more than 1")
	}
	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodecType == "" {
		opt.CodecType = DefaultOption.CodecType
	}
	return opt, nil
}

// dialTimeout if time out return a err
func dialTimeout(f newClientFunc, network, address string, opts ...*Option) (client *Client, err error) {
	// prase connect option
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout(network, address, opt.ConnectTimeout)
	if err != nil {
		return nil, err
	}
	// close the connection if client is nil
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()
	ch := make(chan clientResult)
	go func() {
		client, err := f(conn, opt)
		ch <- clientResult{client: client, err: err}
	}()

	// if ConnectTimeout if zero, infinite waiting it until server call
	if opt.ConnectTimeout == 0 {
		result := <-ch
		return result.client, result.err
	}

	select {
	case <-time.After(opt.ConnectTimeout):
		return nil, fmt.Errorf("rpc client: connect timeout: expect within %s", opt.ConnectTimeout)
	case result := <-ch:
		return result.client, result.err
	}
}

// Dial connects to an RPC server at the specified network address
func Dial(network, address string, opts ...*Option) (*Client, error) {
	return dialTimeout(NewClient, network, address, opts...)
}

// send send request to server
func (client *Client) send(call *Call) {
	// make sure that the client will send a complete request
	client.sending.Lock()
	defer client.sending.Unlock()

	// register this call.
	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}

	// prepare request header
	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = seq
	client.header.Error = ""

	// encode and send the request
	if err := client.cc.Write(&client.header, call.Args); err != nil {
		call := client.removeCall(seq)
		// call may be nil, it usually means that Write partially failed,
		// client has received the response and handled
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

// Go invokes the function asynchronously.
// It returns the Call structure representing the invocation.
func (client *Client) Go(serviceMethod string, args, reply interface{}, done chan *Call) *Call {
	if done == nil {
		done = make(chan *Call, 10)
	} else if cap(done) == 0 {
		log.Panic("rpc client: done channel is unbuffered")
	}
	call := &Call{
		ServiceMethod: serviceMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}
	client.send(call)
	return call
}

// Call invokes the named function, waits for it to complete,
// and returns its error status. user can use ctx to set expire time
func (client *Client) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	call := client.Go(serviceMethod, args, reply, make(chan *Call, 1))
	select {
	case <-ctx.Done():
		client.removeCall(call.Seq)
		return errors.New("rpc client: call failed: " + ctx.Err().Error())
	case call := <-call.Done:
		return call.Error
	}
}
