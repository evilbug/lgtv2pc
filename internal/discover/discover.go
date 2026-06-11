// Package discover localiza TVs LG en la red local por SSDP y resuelve su MAC
// a partir de la tabla ARP del kernel.
package discover

import (
	"bufio"
	"net"
	"os"
	"strings"
	"time"
)

// TV es una TV descubierta.
type TV struct {
	IP     string
	Server string // cabecera SERVER del anuncio SSDP (modelo/SO), si la hay
}

// ssdpAddr es la dirección multicast estándar de SSDP.
const ssdpAddr = "239.255.255.250:1900"

// searchTarget responde solo en TVs LG webOS, así que cualquier respondedor es una TV.
const searchTarget = "urn:lge-com:service:webos-second-screen:1"

// Discover envía un M-SEARCH y recopila las TVs que responden hasta agotar timeout.
func Discover(timeout time.Duration) ([]TV, error) {
	raddr, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	msg := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: " + ssdpAddr + "\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 2\r\n" +
		"ST: " + searchTarget + "\r\n\r\n"

	// Se envía un par de veces: UDP multicast puede perderse.
	for i := 0; i < 2; i++ {
		if _, err := conn.WriteToUDP([]byte(msg), raddr); err != nil {
			return nil, err
		}
	}

	_ = conn.SetReadDeadline(time.Now().Add(timeout))

	found := make(map[string]TV)
	buf := make([]byte, 2048)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			break // normalmente el deadline
		}
		ip := src.IP.String()
		if _, ok := found[ip]; ok {
			continue
		}
		found[ip] = TV{IP: ip, Server: parseHeader(string(buf[:n]), "SERVER")}
	}

	out := make([]TV, 0, len(found))
	for _, tv := range found {
		out = append(out, tv)
	}
	return out, nil
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

// MACForIP busca la MAC asociada a una IP en la tabla ARP del kernel
// (/proc/net/arp). Devuelve "" si no se encuentra (p.ej. aún sin tráfico).
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
			mac := fields[3]
			if mac != "00:00:00:00:00:00" {
				return mac
			}
		}
	}
	return ""
}
