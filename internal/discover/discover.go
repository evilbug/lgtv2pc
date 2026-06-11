// Package discover localiza TVs LG en la red local. Intenta SSDP (rápido pero
// poco fiable: muchos switches/APs filtran multicast) y, como respaldo robusto,
// escanea el /24 local buscando los puertos SSAP (3000/3001) que abren las TVs
// webOS. Además resuelve la MAC desde la tabla ARP del kernel.
package discover

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TV es una TV descubierta.
type TV struct {
	IP     string
	Server string // cabecera SERVER del anuncio SSDP, si la hubo
	Secure bool   // true si solo expone el puerto seguro 3001 (requiere wss)
	MAC    string // resuelta por ARP, si está disponible
}

const (
	ssdpAddr     = "239.255.255.250:1900"
	searchTarget = "urn:lge-com:service:webos-second-screen:1"
	portInsecure = 3000
	portSecure   = 3001
)

// Discover combina SSDP y escaneo de puertos, y enriquece con la MAC por ARP.
func Discover(timeout time.Duration) []TV {
	merged := map[string]*TV{}

	for ip, server := range ssdpSearch(timeout) {
		merged[ip] = &TV{IP: ip, Server: server}
	}
	for ip, tv := range scanSubnet(400 * time.Millisecond) {
		if cur, ok := merged[ip]; ok {
			cur.Secure = tv.Secure
		} else {
			merged[ip] = tv
		}
	}

	out := make([]TV, 0, len(merged))
	for _, tv := range merged {
		tv.MAC = MACForIP(tv.IP)
		out = append(out, *tv)
	}
	return out
}

// ssdpSearch envía un M-SEARCH y recopila los respondedores (ip -> SERVER).
func ssdpSearch(timeout time.Duration) map[string]string {
	out := map[string]string{}
	raddr, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		return out
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return out
	}
	defer conn.Close()

	msg := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: " + ssdpAddr + "\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 2\r\n" +
		"ST: " + searchTarget + "\r\n\r\n"
	for i := 0; i < 2; i++ {
		_, _ = conn.WriteToUDP([]byte(msg), raddr)
	}

	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 2048)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		ip := src.IP.String()
		if _, ok := out[ip]; !ok {
			out[ip] = parseHeader(string(buf[:n]), "SERVER")
		}
	}
	return out
}

// scanSubnet escanea el /24 de la interfaz local buscando puertos SSAP abiertos.
func scanSubnet(perHost time.Duration) map[string]*TV {
	ip := localIPv4()
	if ip == nil {
		return nil
	}
	base := fmt.Sprintf("%d.%d.%d.", ip[0], ip[1], ip[2])

	res := map[string]*TV{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 128)

	portOpen := func(addr string, port int) bool {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(addr, strconv.Itoa(port)), perHost)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}

	for i := 1; i < 255; i++ {
		addr := base + strconv.Itoa(i)
		wg.Add(1)
		sem <- struct{}{}
		go func(addr string) {
			defer wg.Done()
			defer func() { <-sem }()
			has3000 := portOpen(addr, portInsecure)
			has3001 := portOpen(addr, portSecure)
			if !has3000 && !has3001 {
				return
			}
			// Preferir wss/3001 siempre que esté disponible: los webOS modernos
			// mantienen el 3000 abierto a nivel TCP pero resetean el WebSocket
			// sin cifrar, aceptando SSAP solo por 3001. Solo se usa ws/3000 si
			// el 3001 no está (TVs antiguas).
			mu.Lock()
			res[addr] = &TV{IP: addr, Secure: has3001}
			mu.Unlock()
		}(addr)
	}
	wg.Wait()
	return res
}

// localIPv4 devuelve la primera IPv4 no loopback de la máquina.
func localIPv4() net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if v4 := ipnet.IP.To4(); v4 != nil {
				return v4
			}
		}
	}
	return nil
}

// parseHeader extrae el valor de una cabecera HTTP-like de una respuesta SSDP.
func parseHeader(resp, name string) string {
	name = strings.ToLower(name)
	for _, line := range strings.Split(resp, "\r\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.ToLower(strings.TrimSpace(k)) == name {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// MACForIP busca la MAC asociada a una IP en la tabla ARP del kernel.
func MACForIP(ip string) string {
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Scan() // descarta la cabecera
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 4 && fields[0] == ip {
			if mac := fields[3]; mac != "00:00:00:00:00:00" {
				return mac
			}
		}
	}
	return ""
}
