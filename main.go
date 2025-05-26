package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/armon/go-socks5"
)

var ipList []net.IP
var localRand *rand.Rand

func ipToUint32(ip net.IP) uint32 {
	return binary.BigEndian.Uint32(ip.To4())
}

func uint32ToIP(n uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, n)
	return ip
}

func randomIP() net.IP {
	return ipList[localRand.Intn(len(ipList))]
}

func customDialer(ctx context.Context, network, addr string) (net.Conn, error) {
	localIP := randomIP()
	localAddr := &net.TCPAddr{
		IP: localIP,
	}
	dialer := &net.Dialer{
		LocalAddr: localAddr,
		Timeout:   10 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			var err error
			c.Control(func(fd uintptr) {
				err = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_FREEBIND, 1)
			})
			return err
		},
	}
	return dialer.DialContext(ctx, network, addr)
}

func validateIPRange(startStr, endStr string) ([]net.IP, error) {
	startIP := net.ParseIP(startStr).To4()
	endIP := net.ParseIP(endStr).To4()
	if startIP == nil || endIP == nil {
		return nil, fmt.Errorf("invalid IPv4 addresses")
	}

	startVal := ipToUint32(startIP)
	endVal := ipToUint32(endIP)
	if startVal > endVal {
		return nil, fmt.Errorf("start IP must be <= end IP")
	}

	var ips []net.IP
	for i := startVal; i <= endVal; i++ {
		ips = append(ips, uint32ToIP(i))
	}
	return ips, nil
}

func loadIPsFromFile(filePath string) ([]net.IP, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var ips []net.IP
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if ip := net.ParseIP(line).To4(); ip != nil {
			ips = append(ips, ip)
		} else {
			log.Printf("Ignoring invalid IP: %s", line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("no valid IPs found in file")
	}
	return ips, nil
}

func main() {
	startFlag := flag.String("start", "", "Start IP of the range (e.g., 10.1.0.0)")
	endFlag := flag.String("end", "", "End IP of the range (e.g., 10.100.255.255)")
	fileFlag := flag.String("file", "", "File containing a list of IP addresses (one per line)")
	portFlag := flag.Int("port", 1080, "Port on which the SOCKS5 proxy will listen")
	flag.Parse()

	var err error

	switch {
	case *fileFlag != "":
		ipList, err = loadIPsFromFile(*fileFlag)
		if err != nil {
			log.Fatalf("Failed loading IPs from file: %v", err)
		}
		log.Printf("Loaded %d IPs from file", len(ipList))
	case *startFlag != "" && *endFlag != "":
		ipList, err = validateIPRange(*startFlag, *endFlag)
		if err != nil {
			log.Fatalf("Invalid IP range: %v", err)
		}
		log.Printf("Using IP range with %d IPs", len(ipList))
	default:
		log.Fatalf("Usage: -start and -end for IP range OR -file for list of IPs")
	}

	source := rand.NewSource(time.Now().UnixNano())
	localRand = rand.New(source)

	conf := &socks5.Config{
		Dial: customDialer,
	}

	server, err := socks5.New(conf)
	if err != nil {
		log.Fatalf("Error creating SOCKS5 server: %v", err)
	}

	listenAddr := fmt.Sprintf("0.0.0.0:%d", *portFlag)
	log.Printf("Starting SOCKS5 server on %s", listenAddr)
	if err := server.ListenAndServe("tcp", listenAddr); err != nil {
		log.Fatalf("Error starting SOCKS5 server: %v", err)
	}
}
