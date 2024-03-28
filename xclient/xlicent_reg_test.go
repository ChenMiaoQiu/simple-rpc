package xclient

import (
	"context"
	"log"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	simplerpc "github.com/ChenMiaoQiu/simple-rpc"
	"github.com/ChenMiaoQiu/simple-rpc/registry"
)

func startRegistry(wg *sync.WaitGroup) {
	l, _ := net.Listen("tcp", ":9999")
	registry.HandleHTTP()
	wg.Done()
	_ = http.Serve(l, nil)
}

func startRegServer(registryAddr string, wg *sync.WaitGroup) {
	var foo Foo
	l, _ := net.Listen("tcp", ":0")
	server := simplerpc.NewServer()
	_ = server.Register(&foo)
	registry.Heartbeat(registryAddr, "tcp@"+l.Addr().String(), 0)
	wg.Done()
	server.Accept(l)
}

func callReg(registry string) {
	d := NewRegistryDiscovery(registry, 0)
	xc := NewXClient(d, RandomSelect, nil)
	defer func() { _ = xc.Close() }()
	// send request & receive response
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			foo(xc, context.Background(), "call", "Foo.Sum", &Args{Num1: i, Num2: i * i})
		}(i)
	}
	wg.Wait()
}

func broadcastReg(registry string) {
	d := NewRegistryDiscovery(registry, 0)
	xc := NewXClient(d, RandomSelect, nil)
	defer func() { _ = xc.Close() }()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			foo(xc, context.Background(), "broadcast", "Foo.Sum", &Args{Num1: i, Num2: i * i})
			// expect 2 - 5 timeout
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
			foo(xc, ctx, "broadcast", "Foo.Sleep", &Args{Num1: i, Num2: i * i})
			cancel()
		}(i)
	}
	wg.Wait()
}

func Test_XclientReg(t *testing.T) {
	log.SetFlags(0)
	registryAddr := "http://localhost:9999/_geerpc_/registry"
	var wg sync.WaitGroup
	wg.Add(1)
	go startRegistry(&wg)
	wg.Wait()

	time.Sleep(time.Second)
	wg.Add(2)
	go startRegServer(registryAddr, &wg)
	go startRegServer(registryAddr, &wg)
	wg.Wait()

	time.Sleep(time.Second)
	callReg(registryAddr)
	broadcastReg(registryAddr)
}
