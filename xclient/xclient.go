package xclient

import (
	"context"
	"io"
	"log"
	"reflect"
	"sync"

	simplerpc "github.com/ChenMiaoQiu/simple-rpc"
)

type XClient struct {
	d       Discovery         // discovery service method
	mode    SelectMode        // select service method
	opt     *simplerpc.Option // use rpc options
	mu      sync.Mutex        // protect following
	clients map[string]*simplerpc.Client
}

var _ io.Closer = (*XClient)(nil)

// NewXClient return a Xclient by specified discovery, mode, opt
func NewXClient(d Discovery, mode SelectMode, opt *simplerpc.Option) *XClient {
	return &XClient{
		d:       d,
		mode:    mode,
		opt:     opt,
		clients: make(map[string]*simplerpc.Client),
	}
}

// Close close xclient's client
func (xc *XClient) Close() error {
	xc.mu.Lock()
	defer xc.mu.Unlock()
	for key, client := range xc.clients {
		err := client.Close()
		if err != nil {
			log.Println("xclient close client err ", err)
		}
		delete(xc.clients, key)
	}
	return nil
}

// dial return a client. Connect server by specified rpcAddr
// and store it to clientMap when can't find client
func (xc *XClient) dial(rpcAddr string) (*simplerpc.Client, error) {
	xc.mu.Lock()
	defer xc.mu.Unlock()
	// check if client can be connect server
	client, ok := xc.clients[rpcAddr]
	if ok && !client.IsAvailable() {
		_ = client.Close()
		delete(xc.clients, rpcAddr)
		client = nil
	}

	// if can't find server connect server
	if client == nil {
		var err error
		client, err = simplerpc.XDial(rpcAddr, xc.opt)
		if err != nil {
			return nil, err
		}
		xc.clients[rpcAddr] = client
	}

	return client, nil
}

// call connect rpc and handle request
func (xc *XClient) call(rpcAddr string, ctx context.Context, serviceMethod string, args, reply interface{}) error {
	client, err := xc.dial(rpcAddr)
	if err != nil {
		return err
	}
	return client.Call(ctx, serviceMethod, args, reply)
}

// Call invokes the named function, waits for it to complete,
// and returns its error status.
// xc will choose a proper server.
func (xc *XClient) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	rpcAddr, err := xc.d.Get(xc.mode)
	if err != nil {
		return err
	}
	return xc.call(rpcAddr, ctx, serviceMethod, args, reply)
}

// Broadcast invokes the named function for every server registered in discovery
func (xc *XClient) Broadcast(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	servers, err := xc.d.GetAll()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex // protect e and replyDone
	var e error
	replyDone := reply == nil // if reply is nil, don't need to set value
	ctx, cancel := context.WithCancel(ctx)
	for _, rpcAddr := range servers {
		wg.Add(1)
		go func(rpcAddr string) {
			defer wg.Done()
			var clonedReply interface{}
			if reply != nil {
				clonedReply = reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()
			}
			err := xc.call(rpcAddr, ctx, serviceMethod, args, clonedReply)
			mu.Lock()
			if err != nil && e == nil {
				e = err
				cancel() // if any call failed, cancel unfinished calls
			}
			if err == nil && !replyDone {
				reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(clonedReply).Elem())
				replyDone = true
			}
			mu.Unlock()
		}(rpcAddr)
	}
	wg.Wait()
	cancel()
	return e
}
