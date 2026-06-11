package lgtv

import (
	"fmt"
	"net"
)

// WakeOnLAN envía un magic packet a la MAC indicada por broadcast UDP.
// Usado para encender la TV cuando está totalmente apagada (modo "full").
func WakeOnLAN(macStr string) error {
	mac, err := net.ParseMAC(macStr)
	if err != nil {
		return fmt.Errorf("MAC inválida %q: %w", macStr, err)
	}
	if len(mac) != 6 {
		return fmt.Errorf("MAC debe ser de 6 bytes, es de %d", len(mac))
	}

	// Magic packet: 6 bytes 0xFF + 16 repeticiones de la MAC.
	packet := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		packet = append(packet, 0xFF)
	}
	for i := 0; i < 16; i++ {
		packet = append(packet, mac...)
	}

	addr := &net.UDPAddr{IP: net.IPv4bcast, Port: 9}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return fmt.Errorf("abriendo socket WoL: %w", err)
	}
	defer conn.Close()

	if _, err := conn.Write(packet); err != nil {
		return fmt.Errorf("enviando magic packet: %w", err)
	}
	return nil
}
