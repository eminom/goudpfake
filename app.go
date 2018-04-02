package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"sync"
)

var (
	fHop = flag.String("hop", "183.230.40.40:5683", "next hop for fake-proxy")
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

	rAdd, err := net.ResolveUDPAddr("UDP4", realAddr)
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

func (fr *FakeRouter) CheckAndCreate(a string) (chan []byte, bool) {
	fr.esteLock.Lock()
	defer fr.esteLock.Unlock()
	_, ok := fr.thisMap[a]
	esteCltAddr, err := net.ResolveUDPAddr("UDP4", a)
	if err != nil {
		log.Fatalf("client addr resolving: %v", err)
	}
	if !ok {
		ele := &Ele{
			oChan: make(chan []byte),
		}

		var portUsed = fr.localPort
		var esteConn *net.UDPConn
		for p := fr.localPort; ; p++ {
			lAddr, err := net.ResolveUDPAddr("UDP4", fmt.Sprintf(":%v", p))
			if err != nil {
				log.Fatalf("resolve error: %v", err)
			}
			pConn, err := net.ListenUDP("UDP4", lAddr)
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
		go func() {
			buff := make([]byte, 16*1024)
			for {
				read, rAddr, err := esteConn.ReadFromUDP(buff)
				if err != nil {
					continue
				}
				if rAddr.String() == realHost.String() {
					downLink <- dupBuffer(buff[:read])
				}
			}
		}()

		go func() {
			for {
				select {
				case inBuff := <-upLink: // from client
					written, err := esteConn.WriteToUDP(inBuff, realHost)
				case buff := <-downLink:
					fr.writeBack(buff, esteCltAddr)
				}
			}
		}()

	}
	rv, ok := fr.thisMap[a]
	return rv.oChan, ok
}

func main() {
	log.SetFlags(log.Lshortfile)

	addr, err := net.ResolveUDPAddr("udp4", ":5683")
	if err != nil {
		log.Fatalf("resolve addr error: %v", err)
	}

	sock, err := net.ListenUDP("udp4", addr)
	if err != nil {
		log.Fatalf("listen error: %v", err)
	}
	defer sock.Close()

	var wrLock sync.Mutex

	writeFunc := func(buffer []byte, addr *net.UDPAddr) (int, error) {
		wrLock.Lock()
		defer wrLock.Unlock()
		written, err := sock.WriteTo(buffer, addr)
		return written, err
	}

	thisFR := NewFakeRouter(*fHop, writeFunc)
	buffer := make([]byte, 1024*1024)
	for {
		read, rAddr, err := sock.ReadFromUDP(buffer)
		if err != nil {
			//TODO: Fixme
			log.Fatalf("read error: %v", err)
		}
		oChan, ok := thisFR.CheckAndCreate(rAddr.String())
		if ok {
			obuff := make([]byte, read)
			copy(obuff, buffer)
			oChan <- obuff
		}
	}

}
