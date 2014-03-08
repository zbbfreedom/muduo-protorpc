package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"strings"

	"github.com/chenshuo/muduo-examples-in-go/muduo"
	"github.com/chenshuo/muduo-protorpc/go/muduorpc"
)

type options struct {
	port               int
	statusPort         int
	connPerBackend     int
	outstandingPerConn int
	backends           string
}

var opt options

func init() {
	flag.IntVar(&opt.port, "p", 6001, "TCP port")
	flag.IntVar(&opt.statusPort, "s", 6002, "TCP port")
	flag.IntVar(&opt.connPerBackend, "c", 1, "connections per backend")
	flag.IntVar(&opt.outstandingPerConn, "o", 1, "outstanding RPC per connection")
	flag.StringVar(&opt.backends, "b", "", "backends")
}

type balancer struct {
	listener net.Listener
	requests chan request
	backends []backendConn
}

type request struct {
	msg  *muduorpc.RpcMessage
	resp chan *muduorpc.RpcMessage
}

func (b *balancer) ServeConn(c net.Conn) {
	resp := make(chan *muduorpc.RpcMessage, 10) // size ?
	defer close(resp)

	go func() {
		defer c.Close()
		for msg := range resp {
			err := muduorpc.Send(c, msg)
			if err != nil {
				log.Println(err)
				break
			}
		}
		log.Println("close", c.RemoteAddr())
	}()

	for {
		msg, err := muduorpc.Decode(c)
		if err != nil {
			log.Println(err)
			break
		}
		if *msg.Type != *muduorpc.MessageType_REQUEST.Enum() {
			log.Println("Wrong message type.")
			break
		}
		// log.Printf("%v.%v %v req\n", *msg.Service, *msg.Method, *msg.Id)
		req := request{msg: msg, resp: resp}
		b.requests <- req
	}
}

func (b *balancer) dispatch() {
	idx := 0
	for r := range b.requests {
		b.backends[idx].requests <- r
		idx++
		if idx >= len(b.backends) {
			idx = 0
		}
	}
}

type backendConn struct {
	name     string
	conn     net.Conn
	requests chan request
}

func newBackend(addr string) backendConn {
	conn, err := net.Dial("tcp", addr)
	muduo.PanicOnError(err)
	return backendConn{
		name:     addr,
		conn:     conn,
		requests: make(chan request, 10),
	}
}

func (b *backendConn) run() {
	// FIXME: async
	for r := range b.requests {

		//log.Println(b.name, "req", *r.msg.Id)
		err := muduorpc.Send(b.conn, r.msg)
		muduo.PanicOnError(err)
		var resp *muduorpc.RpcMessage
		//log.Println(b.name, "decoding")
		resp, err = muduorpc.Decode(b.conn)
		//log.Println(b.name, "decoded")
		r.resp <- resp
	}
}

func NewBalancer(port int, backendS string) *balancer {
	backends := strings.Split(backendS, ",")
	if len(backends) == 0 || backends[0] == "" {
		log.Fatalln("please specify backends with -b.")
	}
	log.Printf("backends: %q\n", backends)
	s := &balancer{
		requests: make(chan request, 1024), // size ?
		backends: make([]backendConn, len(backends)),
	}
	for i, addr := range backends {
		s.backends[i] = newBackend(addr)
		go s.backends[i].run()
	}
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	muduo.PanicOnError(err)
	s.listener = l
	return s
}

func main() {
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
	s := NewBalancer(opt.port, opt.backends)

	go s.dispatch()
	muduo.ServeTcp(s.listener, s, "balancer")
}
