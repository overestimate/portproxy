package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	CHUNKSIZE int = 16384
)

var (
	WaitingOnTerminate chan chan MapInfo
	Config             Configuration
	HelpList           []string
	OwnIp              string
)

// used for console printing and targeted termination
type MapInfo struct {
	Id       int
	Protocol string
	From     int
	To       int
}

type PortRange struct {
	Start int
	End   int
}

type Mapping struct {
	Disabled     bool
	Protocol     string
	InternalIp   *string
	InternalPort int
	PortOffset   int
}

type Configuration struct {
	PortRange                         PortRange
	AutoPort                          bool
	Mappings                          []Mapping
	AllowLocalhostConnections         bool
	AllowExternalConnectionsFromOwnIp bool
	AllowNotExplicitDenied            bool
	Allowlist                         []string
	Denylist                          []string
}

// sets the Configuration to its default values.
func (c *Configuration) Defaults() {
	c.AllowExternalConnectionsFromOwnIp = true
	c.AllowLocalhostConnections = false
	c.AllowNotExplicitDenied = false
	c.Allowlist = []string{}
	c.Denylist = []string{}
	c.Mappings = []Mapping{}
	c.PortRange = PortRange{
		Start: 54000,
		End:   54099,
	}
}

func GetCurrentIp() string {
	resp, err := http.Get("http://api.ipify.org")
	if err != nil {
		log.Fatalln("unable to get ipify response. error: ", err)
	}
	buf := make([]byte, CHUNKSIZE)
	resBody := make([]byte, 0)
	count := 0
	defer resp.Body.Close()
	for {
		n, err := resp.Body.Read(buf)
		if err != nil && !errors.Is(err, io.EOF) {
			log.Fatalln("unable to read ipify response. error: ", err)
		}
		count += n
		resBody = append(resBody, buf[0:n]...)
		if n != CHUNKSIZE {
			break
		}
	}

	return string(resBody)
}

func IsIpValid(ip string) bool {
	remote := net.ParseIP(ip)
	for _, v := range Config.Denylist {
		if remote.Equal(net.ParseIP(v)) {
			return false
		}
	}
	if Config.AllowNotExplicitDenied {
		return true
	}
	for _, v := range Config.Allowlist {
		if remote.Equal(net.ParseIP(v)) {
			return true
		}
	}
	if remote.Equal(net.ParseIP(OwnIp)) {
		return Config.AllowExternalConnectionsFromOwnIp
	}
	if remote.IsLoopback() {
		return Config.AllowLocalhostConnections
	}
	return false
}

func CloseOnTerminate(closer io.Closer, info MapInfo) {
	waiter := make(chan MapInfo)
	WaitingOnTerminate <- waiter
	waiter <- info
	<-waiter
	if closer == nil {
		return
	}
	closer.Close()
}

func CreateTCPListener(host *string, from int, to int, info MapInfo) {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%v", from))
	if err != nil {
		log.Println("error!", err)
		return
	}
	go CloseOnTerminate(listener, info)
	for {
		conn, err := listener.Accept()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			log.Println("error!", err)
			return
		}
		ip := "127.0.0.1"
		if host != nil {
			ip = *host
		}
		go HandleConnectionTCP(conn, fmt.Sprintf("%v:%v", ip, to), info)
	}

}

func NetworkPipeTCP(from net.Conn, to net.Conn) {
	for {
		res := make([]byte, 0)
		buf := make([]byte, CHUNKSIZE)
		for {
			n, err := from.Read(buf)
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					log.Println("error!", err)
					return
				}
			}
			res = append(res, buf[0:n]...)
			if n != CHUNKSIZE {
				break
			}
		}
		to.Write(res)
	}
}

func HandleConnectionTCP(conn net.Conn, origin string, info MapInfo) {
	if !IsIpValid(conn.RemoteAddr().String()) {
		fmt.Println("invalid ip", conn.RemoteAddr().String())
		return
	}
	fmt.Printf("handling connection from %v to origin %v\n", conn.RemoteAddr(), origin)
	dial, err := net.Dial("tcp", origin)
	if err != nil {
		log.Println("error!", err)
	}
	go CloseOnTerminate(dial, info)
	go NetworkPipeTCP(conn, dial)
	go NetworkPipeTCP(dial, conn)
}

func CreateUDPListener(host *string, from int, to int, info MapInfo) {
	ip := ""
	if host != nil {
		ip = *host
	}
	laddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%v", from))
	if err != nil {
		log.Println("failed opening udp port with error:", err)
		return
	}
	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		log.Println("error!", err)
		log.Println("bye")
		return
	}
	go CloseOnTerminate(conn, info)
	raddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%v:%v", ip, to))
	if err != nil {
		log.Println("error while resolving server. error:", err)
		return
	}
	proxyDials := make(map[string]*net.UDPConn)
	for {
		buf := make([]byte, CHUNKSIZE)
		res := make([]byte, 0)
		var addr *net.UDPAddr
		for {

			n, add, err := conn.ReadFromUDP(buf)
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if err != nil {
				log.Println("error!", err)
				return
			}
			addr = add
			res = append(res, buf[0:n]...)
			if n != CHUNKSIZE {
				break
			}
		}
		log.Println(addr, proxyDials, proxyDials[addr.String()])
		if proxyDials[addr.String()] == nil {
			if !IsIpValid(addr.String()) {
				continue
			}
			dial, err := net.DialUDP("udp", nil, raddr)
			if err != nil {
				log.Println("error!", err)
				continue
			}
			proxyDials[addr.String()] = dial
			go CloseOnTerminate(dial, info)
			go UDPReplyManager(conn, dial, addr)
		}
		proxyDials[addr.String()].Write(res)
	}
}

func UDPReplyManager(conn *net.UDPConn, dial *net.UDPConn, addr *net.UDPAddr) {
	for {
		res := make([]byte, 0)
		buf := make([]byte, CHUNKSIZE)
		for {
			n, err := dial.Read(buf)
			if errors.Is(err, net.ErrClosed) {
				return
			} else if err != nil {
				log.Println("error!", err)
				continue
			}
			res = append(res, buf[0:n]...)
			if n != CHUNKSIZE {
				break
			}
		}
		conn.WriteToUDP(res, addr)
	}
}

func Terminator(killswitch chan bool, target chan MapInfo) {
	chanMap := make(map[MapInfo]chan MapInfo)
	for {
		select {
		case ch := <-WaitingOnTerminate:
			chanMap[<-ch] = ch
		case <-killswitch:
			for info, ch := range chanMap {
				ch <- info
			}
		case m := <-target:
			if chanMap[m] != nil {
				chanMap[m] <- m
			}
		}
	}
}

func init() {
	HelpList = []string{
		"help - prints this help menu",
		"quit - quits this program",
		"mappings - prints all mappings",
		"unmap <id> - unmaps mapping with id. use \"mappings\" to get the id",
		"proxy [ip:]<port> <port> <proto> - proxies ip:port through specified port.\n    ip defaults to 127.0.0.1",
	}
	WaitingOnTerminate = make(chan chan MapInfo)

	Config.Defaults()
	OwnIp = GetCurrentIp()
}

func main() {
	activeMappings := make([]MapInfo, 0)
	configFile, err := os.Open("config.json")
	if err != nil {
		log.Fatalln("config.json not present. please set up portproxy properly")
	}
	defer configFile.Close()
	configJson := make([]byte, CHUNKSIZE)
	buf := make([]byte, CHUNKSIZE)
	total_read := 0
	for {
		n, err := configFile.Read(configJson)
		if err != nil {
			log.Fatalln("error occured while reading config.json:", err)
		}
		total_read += n
		configJson = append(configJson, buf[0:n]...)
		if n != CHUNKSIZE {
			break
		}
	}
	err = json.Unmarshal(configJson[:total_read], &Config)
	if err != nil {
		log.Fatalln("config.json isn't valid! error thrown:", err)
	}

	killswitch := make(chan bool)
	target := make(chan MapInfo)
	go Terminator(killswitch, target)

	counter := 0 // counter for # of proxies, never decremented -- TODO: safely reset if counter gets too high

	basePort := Config.PortRange.Start
	portsAssigned := 0

	for n, mapping := range Config.Mappings {
		port := basePort
		if !Config.AutoPort {
			port += mapping.PortOffset
		} else {
			port += portsAssigned
			portsAssigned++
		}
		info := MapInfo{Protocol: mapping.Protocol, From: mapping.InternalPort, To: port, Id: counter}
		if mapping.Disabled {
			fmt.Println("skipping mapping for protocol", mapping.Protocol, "on port", port)
			continue
		}

		switch mapping.Protocol {
		case "tcp":
			go CreateTCPListener(mapping.InternalIp, port, mapping.InternalPort, info)
		case "udp":
			go CreateUDPListener(mapping.InternalIp, port, mapping.InternalPort, info)
		case "both":
			go CreateTCPListener(mapping.InternalIp, port, mapping.InternalPort, info)
			go CreateUDPListener(mapping.InternalIp, port, mapping.InternalPort, info)
		default:
			log.Fatalln("invalid protocol in mapping at index", n)
		}
		counter++
		activeMappings = append(activeMappings, info)
		log.Println("started listening for protocol", mapping.Protocol, "on port", port)
	}

	fmt.Println("type \"help\" for help, or \"quit\" to quit...")
	for {
		fmt.Print("portproxy> ")
		args := make([]string, 4)
		command := ""
		fmt.Scanln(&command, &args[0], &args[1], &args[2], &args[3])
		if command == "" {
			continue
		}
		parsed_args := make([]string, 0)
		for _, a := range args {
			if a != "" {
				parsed_args = append(parsed_args, a)
			}
		}
		if command == "quit" {
			killswitch <- true
			time.Sleep(1 * time.Second)
			return
		} else if command == "help" {
			fmt.Println("help menu:")
			for _, text := range HelpList {
				fmt.Printf("  %v\n", text)
			}
		} else if command == "proxy" {
			if len(parsed_args) != 3 {
				fmt.Println("invalid amount of arguments")
				continue
			}
			ip_port := strings.Split(args[0], ":")
			if len(ip_port) < 1 || len(ip_port) > 2 {
				fmt.Println("invalid ip:port")
				continue
			}
			if len(ip_port[0]) == 0 {
				ip_port = []string{ip_port[1]}
			}
			var ip *string
			servPort := 0
			if len(ip_port) == 1 {
				servPort, err = strconv.Atoi(ip_port[0])
				if err != nil {
					fmt.Println("invalid source port")
					continue
				}
			} else {
				if net.ParseIP(ip_port[0]) == nil {
					fmt.Println("invalid ip address")
					continue
				}
				ip = &ip_port[0]
				servPort, err = strconv.Atoi(ip_port[1])
				if err != nil {
					fmt.Println("invalid source port")
					continue
				}
			}
			proxPort, err := strconv.Atoi(args[1])
			if err != nil {
				fmt.Println("invalid destination port")
				continue
			}
			info := MapInfo{Protocol: args[2], From: servPort, To: proxPort, Id: counter}
			switch args[2] {
			case "tcp":
				go CreateTCPListener(ip, proxPort, servPort, info)
			case "udp":
				go CreateUDPListener(ip, proxPort, servPort, info)
			case "both":
				go CreateTCPListener(ip, proxPort, servPort, info)
				go CreateUDPListener(ip, proxPort, servPort, info)
			default:
				fmt.Println("invalid protocol")
				continue
			}
			activeMappings = append(activeMappings, info)
			log.Println("started listening for protocol", args[2], "on port", proxPort)
		} else if command == "mappings" {
			fmt.Println("active mappings:")
			for _, m := range activeMappings {
				fmt.Printf(" %v > proxy %v from port %v to port %v\n", m.Id, m.Protocol, m.From, m.To)
			}
		} else if command == "unmap" {
			if args[0] == "" {
				fmt.Println("no id specified")
				continue
			}
			id, err := strconv.Atoi(args[0])
			if err != nil {
				fmt.Println("invalid id")
				continue
			}
			found := false
			for i, m := range activeMappings {
				if m.Id == id {
					target <- m
					found = true
					activeMappings = append(activeMappings[0:i], activeMappings[i+1:]...)
				}
			}
			if found {
				fmt.Printf("terminated proxy with id %v\n", id)
			} else {
				fmt.Println("id not found")
			}
		} else {
			fmt.Println("unknown command")
		}
	}
}
