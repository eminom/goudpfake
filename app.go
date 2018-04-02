package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

var (
	fHop  = flag.String("hop", "183.230.40.40:5683", "next hop for fake-proxy")
	fAddr = flag.String("addr", "127.0.0.1:5683", "local address")
)

type Ele struct {
	oChan chan []byte
}

type FakeRouter struct {
	esteLock  *sync.RWMutex
	thisMap   map[string]Ele
	localPort int

	realHost  *net.UDPAddr
	writeBack func([]byte, *net.UDPAddr) (int, error)
}

func dupBuffer(buffer []byte) []byte {
	a := make([]byte, len(buffer))
	copy(a, buffer)
	return a
}

func NewFakeRouter(realAddr string, writeBack func([]byte, *net.UDPAddr) (int, error)) *FakeRouter {

	rAdd, err := net.ResolveUDPAddr("udp4", realAddr)
	if err != nil {
		log.Fatalf("error resolve real addr: %v", realAddr)
	}

	return &FakeRouter{
		esteLock:  new(sync.RWMutex),
		thisMap:   make(map[string]Ele),
		localPort: 56830,
		realHost:  rAdd,
		writeBack: writeBack,
	}
}

func (fr *FakeRouter) CheckAndCreate(esteCltAddr *net.UDPAddr, wg *sync.WaitGroup, ctx context.Context) (chan []byte, bool) {
	fr.esteLock.Lock()
	defer fr.esteLock.Unlock()

	a := esteCltAddr.String()
	_, ok := fr.thisMap[a]

	if !ok {

		log.Printf("creatin' proxy for %v", esteCltAddr)
		ele := &Ele{
			oChan: make(chan []byte),
		}
		fr.thisMap[a] = *ele

		var portUsed = fr.localPort
		var esteConn *net.UDPConn
		for p := fr.localPort; ; p++ {
			lAddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf(":%v", p))
			if err != nil {
				log.Fatalf("resolve error: %v", err)
			}
			pConn, err := net.ListenUDP("udp4", lAddr)
			if err == nil {
				log.Printf("%d is taken now", p)
				portUsed = p
				esteConn = pConn
				break
			}
		}
		fr.localPort = portUsed + 1

		realHost := fr.realHost
		upLink := ele.oChan

		downLink := make(chan []byte, 1)
		wg.Add(1)
		go func() {
			defer func() {
				log.Printf("leavin read for <%v>", a)
				wg.Done()
			}()
			buff := make([]byte, 16*1024)
			for {
				esteConn.SetReadDeadline(time.Now().Add(1 * time.Second))
				read, rAddr, err := esteConn.ReadFromUDP(buff)
				if err == nil {
					if rAddr.String() == realHost.String() {
						downLink <- dupBuffer(buff[:read])
					}
				} else if nErr, ok := err.(*net.OpError); ok && nErr.Timeout() {
					//As expected.

				} else {
					log.Printf("error: %T: %v", err, err)
				}
				select {
				case <-ctx.Done():
					return
				default:
				}
			}
		}()

		wg.Add(1)
		go func() {
			defer func() {
				log.Printf("leavin for switch <%v>", a)
				wg.Done()
			}()
			for {
				select {
				case <-ctx.Done():
					return
				case inBuff := <-upLink: // from client
					//log.Printf("relay up-link")
					written, ok := esteConn.WriteToUDP(inBuff, realHost)
					_ = written
					_ = ok
				case buff := <-downLink:
					//log.Printf("relay down-link")
					fr.writeBack(buff, esteCltAddr)
				}
			}
		}()

	}
	rv, ok := fr.thisMap[a]
	return rv.oChan, ok
}

func init() {
	flag.Parse()
}

func main() {
	log.SetFlags(log.Lshortfile)

	addr, err := net.ResolveUDPAddr("udp4", *fAddr)
	if err != nil {
		log.Fatalf("resolve addr error: %v", err)
	}

	sock, err := net.ListenUDP("udp4", addr)
	if err != nil {
		log.Fatalf("listen error: %v", err)
	}
	defer sock.Close()

	log.Printf("listening on %v", addr)

	var wrLock sync.Mutex

	writeFunc := func(buffer []byte, addr *net.UDPAddr) (int, error) {
		wrLock.Lock()
		defer wrLock.Unlock()
		written, err := sock.WriteTo(buffer, addr)
		return written, err
	}

	thisFR := NewFakeRouter(*fHop, writeFunc)
	buffer := make([]byte, 1024*1024)

	var wg sync.WaitGroup

	ctx, doCancel := context.WithCancel(context.Background())
	wg.Add(1)
	go func() {
		defer func() {
			log.Printf("leavin master read")
			wg.Done()
		}()
		for {
			sock.SetReadDeadline(time.Now().Add(1 * time.Second))
			read, rAddr, err := sock.ReadFromUDP(buffer)
			if nil == err {
				oChan, ok := thisFR.CheckAndCreate(rAddr, &wg, ctx)
				if ok {
					oChan <- dupBuffer(buffer[:read])
				}
			} else if nErr, ok := err.(*net.OpError); ok && nErr.Timeout() {
				//As expected.
			} else {
				log.Printf("read: %T:%v", err, err)
			}

			select {
			case _ = <-ctx.Done():
				return
			default:
			}

		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, os.Kill, syscall.SIGTERM)
	<-sigCh
	log.Print("quitting")
	doCancel()
	wg.Wait()
	log.Printf("bye")
}
